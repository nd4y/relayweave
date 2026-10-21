package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync/atomic"
	"time"
)

type exportWriter struct {
	out     io.Writer
	queue   chan []byte
	dropped atomic.Int64
}

var dropped atomic.Int64

func Dropped() int64 { return dropped.Load() }

func (w *exportWriter) Write(b []byte) (int, error) {
	n, e := w.out.Write(b)
	if len(b) < 16384 {
		copy := append([]byte(nil), b...)
		select {
		case w.queue <- copy:
		default:
			w.dropped.Add(1)
			dropped.Add(1)
		}
	}
	return n, e
}

// Log export has a fixed queue and timeout and never blocks the data plane.
func Setup(ctx context.Context, service string) {
	url := os.Getenv("LOKI_URL")
	if url == "" {
		slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
		return
	}
	w := &exportWriter{out: os.Stdout, queue: make(chan []byte, 256)}
	slog.SetDefault(slog.New(slog.NewJSONHandler(w, nil)))
	go func() {
		client := &http.Client{Timeout: 2 * time.Second}
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		batch := [][2]string{}
		send := func() {
			if len(batch) == 0 {
				return
			}
			body, _ := json.Marshal(map[string]any{"streams": []any{map[string]any{"stream": map[string]string{"app": "relayweave", "service": service}, "values": batch}}})
			request, e := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
			success := false
			if e == nil {
				request.Header.Set("Content-Type", "application/json")
				if response, e := client.Do(request); e == nil {
					success = response.StatusCode >= 200 && response.StatusCode < 300
					io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
					response.Body.Close()
				}
			}
			if !success {
				dropped.Add(int64(len(batch)))
			}
			batch = batch[:0]
		}
		for {
			select {
			case <-ctx.Done():
				return
			case b := <-w.queue:
				batch = append(batch, [2]string{fmt.Sprint(time.Now().UnixNano()), string(bytes.TrimSpace(b))})
				if len(batch) >= 64 {
					send()
				}
			case <-ticker.C:
				send()
			}
		}
	}()
}
