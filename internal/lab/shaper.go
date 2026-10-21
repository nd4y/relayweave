package lab

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

type paceBucket struct {
	mu   sync.Mutex
	next time.Time
	rate int64
}

func (b *paceBucket) wait(ctx context.Context, n int, rate int64) error {
	if rate <= 0 {
		return fmt.Errorf("fixture_path_disabled")
	}
	now := time.Now()
	b.mu.Lock()
	if b.rate != rate || b.next.Before(now) {
		b.next = now
		b.rate = rate
	}
	b.next = b.next.Add(time.Duration(int64(n) * 8 * int64(time.Second) / (rate * 1000000)))
	delay := time.Until(b.next)
	b.mu.Unlock()
	timer := time.NewTimer(max(0, delay))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func pacedCopy(ctx context.Context, dst, src net.Conn, rate *atomic.Int64, bucket *paceBucket) error {
	buffer := make([]byte, 16<<10)
	for {
		n, e := src.Read(buffer)
		if n > 0 {
			if err := bucket.wait(ctx, n, rate.Load()); err != nil {
				return err
			}
			written, err := dst.Write(buffer[:n])
			if err != nil {
				return err
			}
			if written != n {
				return io.ErrShortWrite
			}
		}
		if e != nil {
			return e
		}
	}
}

// Shaper is a test-only TLS passthrough with a shared per-direction path capacity.
func Shaper(parent context.Context, listen, target, control string, mbps int64) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	var rate atomic.Int64
	rate.Store(mbps)
	down, up := &paceBucket{}, &paceBucket{}
	listener, e := net.Listen("tcp", listen)
	if e != nil {
		return e
	}
	defer listener.Close()
	stop := context.AfterFunc(ctx, func() { listener.Close() })
	defer stop()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /rate", func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			Mbps int64 `json:"mbps"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 128)
		if json.NewDecoder(r.Body).Decode(&v) != nil || v.Mbps < 0 || v.Mbps > 10000 {
			http.Error(w, "invalid capacity", 400)
			return
		}
		rate.Store(v.Mbps)
		json.NewEncoder(w).Encode(v)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]int64{"mbps": rate.Load()})
	})
	server := &http.Server{Addr: control, Handler: mux, ReadHeaderTimeout: 3 * time.Second}
	defer server.Close()
	go func() { server.ListenAndServe(); cancel() }()
	slots := make(chan struct{}, 512)
	for {
		client, e := listener.Accept()
		if e != nil {
			if ctx.Err() != nil {
				return nil
			}
			return e
		}
		if rate.Load() <= 0 {
			client.Close()
			continue
		}
		select {
		case slots <- struct{}{}:
		default:
			client.Close()
			continue
		}
		go func(client net.Conn) {
			defer func() { <-slots }()
			defer client.Close()
			upstream, e := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", target)
			if e != nil {
				return
			}
			defer upstream.Close()
			stop := context.AfterFunc(ctx, func() { client.Close(); upstream.Close() })
			defer stop()
			done := make(chan struct{})
			go func() {
				defer close(done)
				pacedCopy(ctx, upstream, client, &rate, up)
				if c, ok := upstream.(*net.TCPConn); ok {
					c.CloseWrite()
				}
			}()
			pacedCopy(ctx, client, upstream, &rate, down)
			client.Close()
			upstream.Close()
			<-done
		}(client)
	}
}
