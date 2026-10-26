// Package socks implements the bounded framing shared by the gateway and its Xray adapter.
package socks

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

func Write(w io.Writer, b []byte) error {
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
func Address(host string, port int) ([]byte, error) {
	if port < 0 || port > 65535 {
		return nil, errors.New("invalid port")
	}
	var b []byte
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			b = append([]byte{1}, v4...)
		} else {
			b = append([]byte{4}, ip.To16()...)
		}
	} else {
		if len(host) == 0 || len(host) > 255 {
			return nil, errors.New("invalid host")
		}
		b = append([]byte{3, byte(len(host))}, []byte(host)...)
	}
	return append(b, byte(port>>8), byte(port)), nil
}
func ReadAddress(r io.Reader) (host string, port int, raw []byte, err error) {
	var t [1]byte
	if _, err = io.ReadFull(r, t[:]); err != nil {
		return
	}
	raw = append(raw, t[0])
	n := 0
	switch t[0] {
	case 1:
		n = 4
	case 4:
		n = 16
	case 3:
		var l [1]byte
		if _, err = io.ReadFull(r, l[:]); err != nil {
			return
		}
		if l[0] == 0 {
			err = errors.New("empty domain")
			return
		}
		raw = append(raw, l[0])
		n = int(l[0])
	default:
		err = errors.New("unsupported address type")
		return
	}
	b := make([]byte, n+2)
	if _, err = io.ReadFull(r, b); err != nil {
		return
	}
	raw = append(raw, b...)
	port = int(binary.BigEndian.Uint16(b[n:]))
	if t[0] == 3 {
		host = string(b[:n])
	} else {
		host = net.IP(b[:n]).String()
	}
	return
}
func Request(ctx context.Context, endpoint string, command byte, host string, port int, timeout time.Duration) (net.Conn, string, int, error) {
	c, e := (&net.Dialer{Timeout: timeout}).DialContext(ctx, "tcp", endpoint)
	if e != nil {
		return nil, "", 0, e
	}
	ok := false
	defer func() {
		if !ok {
			c.Close()
		}
	}()
	deadline := time.Now().Add(timeout)
	if d, exists := ctx.Deadline(); exists && d.Before(deadline) {
		deadline = d
	}
	c.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	if e = Write(c, []byte{5, 1, 0}); e != nil {
		return nil, "", 0, e
	}
	var reply [2]byte
	if _, e = io.ReadFull(c, reply[:]); e != nil {
		return nil, "", 0, e
	}
	if reply != [2]byte{5, 0} {
		return nil, "", 0, errors.New("upstream authentication rejected")
	}
	a, e := Address(host, port)
	if e != nil {
		return nil, "", 0, e
	}
	if e = Write(c, append([]byte{5, command, 0}, a...)); e != nil {
		return nil, "", 0, e
	}
	var h [3]byte
	if _, e = io.ReadFull(c, h[:]); e != nil {
		return nil, "", 0, e
	}
	if h[0] != 5 || h[1] != 0 || h[2] != 0 {
		return nil, "", 0, ReplyError{Code: h[1]}
	}
	bh, bp, _, e := ReadAddress(c)
	if e != nil {
		return nil, "", 0, e
	}
	c.SetDeadline(time.Time{})
	ok = true
	return c, bh, bp, nil
}

type ReplyError struct{ Code byte }

func (e ReplyError) Error() string { return fmt.Sprintf("upstream SOCKS failure %d", e.Code) }
func Dial(ctx context.Context, endpoint, target string, timeout time.Duration) (net.Conn, error) {
	h, p, e := net.SplitHostPort(target)
	if e != nil {
		return nil, e
	}
	port, e := strconv.Atoi(p)
	if e != nil {
		return nil, e
	}
	c, _, _, e := Request(ctx, endpoint, 1, h, port, timeout)
	return c, e
}
func Reply(c net.Conn, code byte, host string, port int) error {
	a, e := Address(host, port)
	if e != nil {
		return e
	}
	return Write(c, append([]byte{5, code, 0}, a...))
}

func ValidateDatagram(b []byte) error {
	if len(b) < 7 || b[0] != 0 || b[1] != 0 || b[2] != 0 {
		return errors.New("invalid or fragmented UDP datagram")
	}
	n := 0
	switch b[3] {
	case 1:
		n = 4
	case 4:
		n = 16
	case 3:
		if len(b) < 5 || b[4] == 0 {
			return errors.New("invalid domain")
		}
		n = 1 + int(b[4])
	default:
		return errors.New("invalid address type")
	}
	if len(b) < 4+n+2 {
		return io.ErrUnexpectedEOF
	}
	return nil
}
