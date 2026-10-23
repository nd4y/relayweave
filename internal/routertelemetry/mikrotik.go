package routertelemetry

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

func Builtins() Registry {
	return Registry{"mikrotik-api-ssl": {Descriptor: Descriptor{ID: "mikrotik-api-ssl", Version: 1, Capabilities: []string{"wan.counters", "wan.link_state", "router.cpu"}}, New: newMikrotik}}
}

type mikrotik struct {
	cfg       RouterConfig
	tlsConfig *tls.Config
	password  string
	mu        sync.Mutex
	conn      net.Conn
	reader    *bufio.Reader
}

func newMikrotik(cfg RouterConfig) (Adapter, error) {
	host, port, err := net.SplitHostPort(cfg.Endpoint)
	if err != nil || net.ParseIP(host) == nil || port == "" || cfg.ServerName == "" || cfg.Username == "" {
		return nil, errors.New("explicit router IP, TLS server name and username required")
	}
	password, err := ReadSecret(cfg.PasswordFile)
	if err != nil {
		return nil, err
	}
	pem, err := os.ReadFile(cfg.CAFile)
	if err != nil || len(pem) > 256<<10 {
		return nil, errors.New("cannot read router CA")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("invalid router CA")
	}
	return &mikrotik{cfg: cfg, password: password, tlsConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool, ServerName: cfg.ServerName}}, nil
}

func (a *mikrotik) Close() error { a.mu.Lock(); defer a.mu.Unlock(); return a.close() }
func (a *mikrotik) close() error {
	if a.conn != nil {
		err := a.conn.Close()
		a.conn = nil
		a.reader = nil
		return err
	}
	return nil
}

func (a *mikrotik) Read(ctx context.Context) (result Reading, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	defer func() {
		if err != nil {
			_ = a.close()
		}
	}()
	deadline, ok := ctx.Deadline()
	if !ok {
		return result, errors.New("router read requires bounded context")
	}
	if a.conn == nil {
		dialer := tls.Dialer{NetDialer: &net.Dialer{Timeout: time.Second, KeepAlive: 30 * time.Second}, Config: a.tlsConfig}
		a.conn, err = dialer.DialContext(ctx, "tcp", a.cfg.Endpoint)
		if err != nil {
			return result, errors.New("router TLS connection failed")
		}
		a.reader = bufio.NewReader(a.conn)
		if err = a.conn.SetDeadline(deadline); err != nil {
			return result, err
		}
		if _, err = a.command([]string{"/login", "=name=" + a.cfg.Username, "=password=" + a.password}); err != nil {
			return result, errors.New("router authentication failed")
		}
	}
	if err = a.conn.SetDeadline(deadline); err != nil {
		return result, err
	}
	conn := a.conn
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()
	rows, err := a.command([]string{"/system/resource/print", "=.proplist=cpu-load"})
	if err != nil {
		return result, err
	}
	if len(rows) != 1 {
		return result, errors.New("invalid router resource response")
	}
	cpu, e := strconv.ParseFloat(rows[0]["cpu-load"], 64)
	if e != nil || math.IsNaN(cpu) || math.IsInf(cpu, 0) || cpu < 0 || cpu > 100 {
		return result, errors.New("invalid router CPU metric")
	}
	result.CPUPercent = &cpu
	for _, w := range a.cfg.WANs {
		rows, err = a.command([]string{"/interface/print", "=stats=", "=.proplist=name,running,disabled,rx-byte,tx-byte", "?name=" + w.Interface})
		if err != nil {
			return result, err
		}
		if len(rows) != 1 || rows[0]["name"] != w.Interface {
			return result, errors.New("configured WAN interface not found")
		}
		r := rows[0]
		rx, e := strconv.ParseUint(r["rx-byte"], 10, 64)
		if e != nil {
			return result, errors.New("invalid router RX counter")
		}
		tx, e := strconv.ParseUint(r["tx-byte"], 10, 64)
		if e != nil {
			return result, errors.New("invalid router TX counter")
		}
		if (r["running"] != "true" && r["running"] != "false") || (r["disabled"] != "true" && r["disabled"] != "false") {
			return result, errors.New("invalid router link state")
		}
		result.WANs = append(result.WANs, WAN{ID: w.ID, ISP: w.ISP, Interface: w.Interface, Running: r["running"] == "true", Disabled: r["disabled"] == "true", RXBytes: rx, TXBytes: tx})
	}
	return result, nil
}

// An allowlist is independent of RouterOS group permissions. No request from the
// HTTP API or config can select a command, file operation, script or mutation.
func (a *mikrotik) command(words []string) ([]map[string]string, error) {
	if len(words) == 0 {
		return nil, errors.New("empty router command")
	}
	switch words[0] {
	case "/login", "/interface/print", "/system/resource/print":
	default:
		return nil, errors.New("router command is not allowlisted")
	}
	for _, w := range words {
		if err := writeWord(a.conn, w); err != nil {
			return nil, errors.New("router write failed")
		}
	}
	if _, err := a.conn.Write([]byte{0}); err != nil {
		return nil, errors.New("router write failed")
	}
	rows := []map[string]string{}
	trapped := false
	for count := 0; count < 32; count++ {
		sentence, err := readSentence(a.reader)
		if err != nil {
			return nil, errors.New("invalid router response")
		}
		if len(sentence) == 0 {
			continue
		}
		switch sentence[0] {
		case "!done":
			if trapped {
				return nil, errors.New("router command denied")
			}
			return rows, nil
		case "!trap":
			trapped = true
		case "!fatal":
			return nil, errors.New("router session closed")
		case "!re":
			if len(rows) >= 4 {
				return nil, errors.New("router response row limit exceeded")
			}
			r := map[string]string{}
			for _, word := range sentence[1:] {
				if strings.HasPrefix(word, "=") {
					k, v, ok := strings.Cut(word[1:], "=")
					if ok {
						r[k] = v
					}
				}
			}
			rows = append(rows, r)
		default:
			return nil, errors.New("unexpected router response")
		}
	}
	return nil, errors.New("router response sentence limit exceeded")
}

func writeWord(w io.Writer, s string) error {
	n := len(s)
	var prefix []byte
	switch {
	case n < 0x80:
		prefix = []byte{byte(n)}
	case n < 0x4000:
		prefix = []byte{byte(n>>8) | 0x80, byte(n)}
	case n < 0x200000:
		prefix = []byte{byte(n>>16) | 0xc0, byte(n >> 8), byte(n)}
	default:
		return errors.New("outbound router word too large")
	}
	b := append(prefix, []byte(s)...)
	for len(b) > 0 {
		n, e := w.Write(b)
		if e != nil {
			return e
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}

func readSentence(r *bufio.Reader) ([]string, error) {
	words := []string{}
	total := 0
	for len(words) < 64 {
		b, err := r.ReadByte()
		if err != nil {
			return nil, err
		}
		n := uint32(b)
		switch {
		case b < 0x80:
		case b < 0xc0:
			v, e := r.ReadByte()
			if e != nil {
				return nil, e
			}
			n = (uint32(b&0x3f) << 8) | uint32(v)
		case b < 0xe0:
			var buf [2]byte
			if _, e := io.ReadFull(r, buf[:]); e != nil {
				return nil, e
			}
			n = (uint32(b&0x1f) << 16) | uint32(binary.BigEndian.Uint16(buf[:]))
		case b < 0xf0:
			var buf [3]byte
			if _, e := io.ReadFull(r, buf[:]); e != nil {
				return nil, e
			}
			n = (uint32(b&0x0f) << 24) | (uint32(buf[0]) << 16) | (uint32(buf[1]) << 8) | uint32(buf[2])
		case b == 0xf0:
			var buf [4]byte
			if _, e := io.ReadFull(r, buf[:]); e != nil {
				return nil, e
			}
			n = binary.BigEndian.Uint32(buf[:])
		default:
			return nil, errors.New("reserved router word prefix")
		}
		if n == 0 {
			return words, nil
		}
		if n > 65536 || uint64(total)+uint64(n) > 128<<10 {
			return nil, errors.New("router response too large")
		}
		buf := make([]byte, int(n))
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		total += len(buf)
		words = append(words, string(buf))
	}
	return nil, fmt.Errorf("router response word count exceeded")
}
