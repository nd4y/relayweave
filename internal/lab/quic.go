package lab

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	quic "github.com/quic-go/quic-go"
	"io"
	"net"
	"os"
	"proxyplatform/internal/socks"
	"strconv"
	"sync"
	"time"
)

func QUICOrigin(ctx context.Context, dir string) error {
	certificate, e := tls.LoadX509KeyPair(dir+"/ca.pem", dir+"/key.pem")
	if e != nil {
		return e
	}
	listener, e := quic.ListenAddr(":9444", &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificate}, NextProtos: []string{"relayweave-lab-v1"}}, &quic.Config{MaxIdleTimeout: 15 * time.Second})
	if e != nil {
		return e
	}
	defer listener.Close()
	for {
		conn, e := listener.Accept(ctx)
		if e != nil {
			if ctx.Err() != nil {
				return nil
			}
			return e
		}
		go func() {
			defer conn.CloseWithError(0, "")
			for {
				stream, e := conn.AcceptStream(ctx)
				if e != nil {
					return
				}
				go func() { defer stream.Close(); io.Copy(stream, io.LimitReader(stream, 1<<20)) }()
			}
		}()
	}
}

type socksPacketConn struct {
	socket  *net.UDPConn
	control net.Conn
	target  []byte
	peer    *net.UDPAddr
	once    sync.Once
}

func (c *socksPacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	buf := make([]byte, 8192)
	for {
		n, e := c.socket.Read(buf)
		if e != nil {
			return 0, nil, e
		}
		if socks.ValidateDatagram(buf[:n]) != nil {
			continue
		}
		reader := bytes.NewReader(buf[3:n])
		_, port, raw, e := socks.ReadAddress(reader)
		if e != nil || port != 9444 {
			continue
		}
		payload := buf[3+len(raw) : n]
		if len(payload) > len(b) {
			return 0, nil, io.ErrShortBuffer
		}
		return copy(b, payload), c.peer, nil
	}
}
func (c *socksPacketConn) WriteTo(b []byte, _ net.Addr) (int, error) {
	frame := append(append([]byte{0, 0, 0}, c.target...), b...)
	if len(frame) > 7168 {
		return 0, fmt.Errorf("datagram_too_large")
	}
	_, e := c.socket.Write(frame)
	if e != nil {
		return 0, e
	}
	return len(b), nil
}
func (c *socksPacketConn) Close() error {
	c.once.Do(func() { c.socket.Close(); c.control.Close() })
	return nil
}
func (c *socksPacketConn) LocalAddr() net.Addr                { return c.socket.LocalAddr() }
func (c *socksPacketConn) SetDeadline(t time.Time) error      { return c.socket.SetDeadline(t) }
func (c *socksPacketConn) SetReadDeadline(t time.Time) error  { return c.socket.SetReadDeadline(t) }
func (c *socksPacketConn) SetWriteDeadline(t time.Time) error { return c.socket.SetWriteDeadline(t) }
func (c *socksPacketConn) SetReadBuffer(n int) error          { return c.socket.SetReadBuffer(n) }
func (c *socksPacketConn) SetWriteBuffer(n int) error         { return c.socket.SetWriteBuffer(n) }
func QUICClient(ctx context.Context, proxy, user, password, caPath string) error {
	control, e := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", proxy)
	if e != nil {
		return e
	}
	defer control.Close()
	control.SetDeadline(time.Now().Add(8 * time.Second))
	method := byte(0)
	if user != "" {
		method = 2
	}
	if e = socks.Write(control, []byte{5, 1, method}); e != nil {
		return e
	}
	var reply [2]byte
	if _, e = io.ReadFull(control, reply[:]); e != nil || reply != [2]byte{5, method} {
		return fmt.Errorf("QUIC SOCKS greeting failed")
	}
	if method == 2 {
		if len(user) > 255 || len(password) > 255 {
			return fmt.Errorf("credentials too long")
		}
		auth := append([]byte{1, byte(len(user))}, []byte(user)...)
		auth = append(auth, byte(len(password)))
		auth = append(auth, []byte(password)...)
		if e = socks.Write(control, auth); e != nil {
			return e
		}
		if _, e = io.ReadFull(control, reply[:]); e != nil || reply != [2]byte{1, 0} {
			return fmt.Errorf("QUIC SOCKS auth failed")
		}
	}
	addr, _ := socks.Address("0.0.0.0", 0)
	if e = socks.Write(control, append([]byte{5, 3, 0}, addr...)); e != nil {
		return e
	}
	var h [3]byte
	if _, e = io.ReadFull(control, h[:]); e != nil || h != [3]byte{5, 0, 0} {
		return fmt.Errorf("QUIC UDP association failed")
	}
	host, port, _, e := socks.ReadAddress(control)
	if e != nil {
		return e
	}
	control.SetDeadline(time.Time{})
	relay, e := net.ResolveUDPAddr("udp", net.JoinHostPort(host, strconv.Itoa(port)))
	if e != nil {
		return e
	}
	udp, e := net.DialUDP("udp", nil, relay)
	if e != nil {
		return e
	}
	target, _ := socks.Address("origin", 9444)
	packet := &socksPacketConn{socket: udp, control: control, target: target, peer: &net.UDPAddr{IP: net.IPv4(198, 18, 0, 1), Port: 9444}}
	defer packet.Close()
	roots := x509.NewCertPool()
	data, e := os.ReadFile(caPath)
	if e != nil || !roots.AppendCertsFromPEM(data) {
		return fmt.Errorf("QUIC CA unavailable")
	}
	conn, e := quic.Dial(ctx, packet, packet.peer, &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: "origin", NextProtos: []string{"relayweave-lab-v1"}}, &quic.Config{HandshakeIdleTimeout: 8 * time.Second, MaxIdleTimeout: 10 * time.Second, DisablePathMTUDiscovery: true, InitialPacketSize: 1200})
	if e != nil {
		return e
	}
	defer conn.CloseWithError(0, "")
	stream, e := conn.OpenStreamSync(ctx)
	if e != nil {
		return e
	}
	payload := bytes.Repeat([]byte("QUIC-through-SOCKS5-"), 2048)
	if e = socks.Write(stream, payload); e != nil {
		return e
	}
	if e = stream.Close(); e != nil {
		return e
	}
	got, e := io.ReadAll(io.LimitReader(stream, 1<<20))
	if e != nil {
		return e
	}
	if !bytes.Equal(got, payload) {
		return fmt.Errorf("QUIC payload mismatch")
	}
	return nil
}
