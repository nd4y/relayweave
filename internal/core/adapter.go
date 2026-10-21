package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"proxyplatform/internal/config"
	"proxyplatform/internal/socks"
)

type Capabilities struct {
	Protocols, Transports, Security []string
	TCP, UDP                        bool
}
type Packet struct {
	Host    string
	Port    int
	Payload []byte
}

// PacketSession exchanges application datagrams. SOCKS framing is private to
// the Xray implementation; another engine can use its own packet transport.
// ReadPacket may borrow the supplied buffer until the next read.
type PacketSession interface {
	ReadPacket([]byte) (Packet, error)
	WritePacket(Packet) error
	SetReadDeadline(time.Time) error
	Done() <-chan struct{}
	Close() error
}

// The supervisor owns lifecycle, health state, counters and drain. Adapters own
// capabilities, native configuration/validation, commands and data connections.
type Adapter interface {
	Family() string
	Capabilities() Capabilities
	Validate(config.Route) error
	RenderConfig([]config.Route) ([]byte, error)
	Version(context.Context, string, string) (string, error)
	TestArgs(string) []string
	RunArgs(string) []string
	OpenTCP(context.Context, string, string, time.Duration) (net.Conn, error)
	OpenUDP(context.Context, string, time.Duration) (PacketSession, error)
}

var adapterMu sync.RWMutex
var adapters = map[string]Adapter{"xray": XrayAdapter{}}

// RegisterAdapter is a code extension point, not a runtime binary/plugin loader.
func RegisterAdapter(adapter Adapter) error {
	adapterMu.Lock()
	defer adapterMu.Unlock()
	if adapter == nil || adapter.Family() == "" || adapters[adapter.Family()] != nil {
		return fmt.Errorf("engine_adapter_already_registered_or_invalid")
	}
	adapters[adapter.Family()] = adapter
	return nil
}
func AdapterFor(family string) (Adapter, error) {
	if family == "" {
		family = "xray"
	}
	adapterMu.RLock()
	defer adapterMu.RUnlock()
	adapter := adapters[family]
	if adapter == nil {
		return nil, fmt.Errorf("engine_family_not_supported")
	}
	return adapter, nil
}
func (i *Instance) Adapter() Adapter { a, _ := AdapterFor(i.Config.Family); return a }

type XrayAdapter struct{}

func (XrayAdapter) Family() string { return "xray" }
func (XrayAdapter) Capabilities() Capabilities {
	return Capabilities{TCP: true, UDP: true, Protocols: []string{"vless", "vmess", "trojan", "shadowsocks", "http", "socks"}, Transports: []string{"raw", "tcp", "ws", "grpc", "httpupgrade", "xhttp"}, Security: []string{"none", "tls", "reality"}}
}
func (XrayAdapter) Validate(r config.Route) error {
	if !json.Valid(r.Outbound) {
		return fmt.Errorf("engine_native_config_invalid")
	}
	return nil
}
func (XrayAdapter) RenderConfig(routes []config.Route) ([]byte, error) { return Render(routes) }
func (XrayAdapter) Version(ctx context.Context, path, version string) (string, error) {
	return Version(ctx, path, version)
}
func (XrayAdapter) TestArgs(path string) []string { return []string{"run", "-test", "-config", path} }
func (XrayAdapter) RunArgs(path string) []string  { return []string{"run", "-config", path} }
func (XrayAdapter) OpenTCP(ctx context.Context, endpoint, target string, timeout time.Duration) (net.Conn, error) {
	return socks.Dial(ctx, endpoint, target, timeout)
}

type xrayPackets struct {
	control net.Conn
	udp     *net.UDPConn
	done    chan struct{}
	once    sync.Once
}

func (XrayAdapter) OpenUDP(ctx context.Context, endpoint string, timeout time.Duration) (PacketSession, error) {
	control, host, port, err := socks.Request(ctx, endpoint, 3, "0.0.0.0", 0, timeout)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (PacketSession, error) { control.Close(); return nil, err }
	if ip := net.ParseIP(host); ip == nil || ip.IsUnspecified() {
		host, _, _ = net.SplitHostPort(control.RemoteAddr().String())
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() || port <= 0 {
		return fail(fmt.Errorf("engine_udp_endpoint_not_private"))
	}
	addr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return fail(err)
	}
	udp, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		return fail(err)
	}
	session := &xrayPackets{control: control, udp: udp, done: make(chan struct{})}
	go func() {
		select {
		case <-ctx.Done():
			session.Close()
		case <-session.Done():
		}
	}()
	go func() { io.Copy(io.Discard, control); session.Close() }()
	return session, nil
}
func (s *xrayPackets) Done() <-chan struct{}             { return s.done }
func (s *xrayPackets) SetReadDeadline(t time.Time) error { return s.udp.SetReadDeadline(t) }
func (s *xrayPackets) Close() error {
	s.once.Do(func() { s.control.Close(); s.udp.Close(); close(s.done) })
	return nil
}
func (s *xrayPackets) ReadPacket(buffer []byte) (Packet, error) {
	n, err := s.udp.Read(buffer)
	if err != nil {
		return Packet{}, err
	}
	if socks.ValidateDatagram(buffer[:n]) != nil {
		return Packet{}, fmt.Errorf("engine_udp_frame_invalid")
	}
	host, port, _, err := socks.ReadAddress(bytes.NewReader(buffer[3:n]))
	if err != nil {
		return Packet{}, err
	}
	address, err := socks.Address(host, port)
	if err != nil {
		return Packet{}, err
	}
	return Packet{Host: host, Port: port, Payload: buffer[3+len(address) : n]}, nil
}
func (s *xrayPackets) WritePacket(p Packet) error {
	address, err := socks.Address(p.Host, p.Port)
	if err != nil {
		return err
	}
	frame := make([]byte, 3+len(address)+len(p.Payload))
	copy(frame[3:], address)
	copy(frame[3+len(address):], p.Payload)
	_, err = s.udp.Write(frame)
	return err
}
