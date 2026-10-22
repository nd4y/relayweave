package compute

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"time"
)

// Client never logs request bodies, identity keys, or attempt capabilities.
type Client struct {
	URL         string
	HTTP        *http.Client
	IdentityDir string
}

func NewClient(address string, config *tls.Config) (*Client, error) {
	u, e := url.Parse(address)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || config == nil || config.InsecureSkipVerify {
		return nil, ErrInvalid
	}
	transport := &http.Transport{DialContext: (&net.Dialer{Timeout: 2 * time.Second, KeepAlive: 30 * time.Second}).DialContext, TLSClientConfig: config.Clone(), TLSHandshakeTimeout: 2 * time.Second, ResponseHeaderTimeout: 20 * time.Second, MaxResponseHeaderBytes: 8 << 10, MaxConnsPerHost: 2, MaxIdleConnsPerHost: 2, DisableCompression: true}
	return &Client{URL: u.Scheme + "://" + u.Host, HTTP: &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) post(ctx context.Context, path, token string, input, output any) (int, error) {
	body, e := Canonical(input)
	if e != nil {
		return 0, e
	}
	if len(body) > 32<<10 {
		return 0, ErrInvalid
	}
	req, e := http.NewRequestWithContext(ctx, "POST", c.URL+path, bytes.NewReader(body))
	if e != nil {
		return 0, e
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	response, e := c.HTTP.Do(req)
	if e != nil {
		return 0, e
	}
	defer response.Body.Close()
	data, e := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if e != nil || len(data) > 64<<10 {
		return response.StatusCode, ErrInvalid
	}
	if response.StatusCode == 204 {
		return 204, nil
	}
	if response.StatusCode != 200 {
		if response.StatusCode == 429 {
			if response.Header.Get("Retry-After") == "86400" {
				return 429, ErrDailyBudget
			}
			return 429, ErrBudget
		}
		return response.StatusCode, ErrStale
	}
	if output != nil {
		e = StrictDecode(data, output)
	}
	return response.StatusCode, e
}

func (c *Client) Run(ctx context.Context, execute func(context.Context, Task) (Result, error)) error {
	session := ""
	backoff := time.Second
	defer c.HTTP.CloseIdleConnections()
	for ctx.Err() == nil {
		if c.IdentityDir != "" {
			if e := c.RenewIdentity(ctx, c.IdentityDir); e != nil {
				return e
			}
		}
		if session == "" {
			var reply registerResponse
			code, e := c.post(ctx, "/compute/v1/register", "", sessionRequest{Version: 1}, &reply)
			if e != nil {
				if code == 403 {
					return ErrStale
				}
				if !sleep(ctx, retryDelay(e, backoff)) {
					break
				}
				backoff = min(30*time.Second, backoff*2)
				continue
			}
			session = reply.Session
			backoff = time.Second
		}
		var heartbeat map[string]any
		code, e := c.post(ctx, "/compute/v1/heartbeat", "", sessionRequest{Version: 1, Session: session}, &heartbeat)
		if e != nil {
			if code == 403 {
				return ErrStale
			}
			session = ""
			if !sleep(ctx, retryDelay(e, backoff)) {
				break
			}
			continue
		}
		request := sessionRequest{Version: 1, Session: session, Request: newID()}
		var task Task
		code, e = c.post(ctx, "/compute/v1/lease", "", request, &task)
		if e != nil && code == 0 && ctx.Err() == nil {
			code, e = c.post(ctx, "/compute/v1/lease", "", request, &task)
		}
		if e != nil {
			if code == 410 {
				session = ""
			}
			if !sleep(ctx, retryDelay(e, backoff)) {
				break
			}
			continue
		}
		if code == 204 {
			if !sleep(ctx, time.Second) {
				break
			}
			continue
		}
		if e = task.Validate(); e != nil {
			return e
		}
		job, cancel := context.WithTimeout(ctx, time.Duration(task.WallBudgetMS)*time.Millisecond)
		result, e := execute(job, task)
		cancel()
		if e != nil {
			result = Result{Version: 1, OwnerEpoch: task.OwnerEpoch, Task: task.ID, Attempt: task.Attempt, Session: session, SnapshotDigest: task.SnapshotDigest, Status: "error"}
		}
		// Lost result response reuses the exact attempt/body/token. Never
		// execute the same task again simply because an acknowledgement was lost.
		for attempt := 0; attempt < 2; attempt++ {
			code, e = c.post(ctx, "/compute/v1/result", task.Token, result, nil)
			if e == nil || code == 400 || code == 403 || code == 409 || code == 410 {
				break
			}
			if !sleep(ctx, time.Second) {
				break
			}
		}
		if !sleep(ctx, retryDelay(e, 5*time.Second)) {
			break
		}
	}
	return ctx.Err()
}
func retryDelay(e error, fallback time.Duration) time.Duration {
	if errors.Is(e, ErrDailyBudget) {
		return 24 * time.Hour
	}
	if errors.Is(e, ErrBudget) {
		return 10 * time.Second
	}
	return fallback
}

func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// ExecuteChild provides process isolation. The host/container/Job Object cap
// must already cover this parent and descendants before calling it.
func ExecuteChild(ctx context.Context, task Task) (Result, error) {
	var result Result
	self, e := os.Executable()
	if e != nil {
		return result, e
	}
	return executeBinary(ctx, self, task)
}

func executeBinary(ctx context.Context, self string, task Task) (Result, error) {
	var result Result
	ctx, cancel, e := elapsedDeadline(ctx, time.Duration(task.WallBudgetMS)*time.Millisecond)
	if e != nil {
		return result, e
	}
	defer cancel()
	raw, e := Canonical(task)
	if e != nil {
		return result, e
	}
	cmd := exec.CommandContext(ctx, self, "evaluate")
	cmd.Stdin = bytes.NewReader(raw)
	var out limitedBuffer
	out.limit = 32 << 10
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	cmd.Env = []string{"GOMAXPROCS=1", "GOMEMLIMIT=384MiB"}
	// Windows needs these runtime paths; never inherit proxy/service secrets.
	for _, name := range []string{"SystemRoot", "WINDIR", "TEMP", "TMP"} {
		if value := os.Getenv(name); value != "" {
			cmd.Env = append(cmd.Env, name+"="+value)
		}
	}
	if e = cmd.Run(); e != nil {
		return result, ErrStale
	}
	if out.overflow {
		return result, ErrInvalid
	}
	if e = StrictDecode(out.Bytes(), &result); e != nil {
		return result, e
	}
	return result, nil
}

type limitedBuffer struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		b.overflow = true
		return 0, ErrInvalid
	}
	return b.Buffer.Write(p)
}

// EvaluateProcess is the only child command. Its own CPU timer includes decode
// and Go startup CPU. The search checks cancellation at every scenario.
func EvaluateProcess(ctx context.Context, input io.Reader, output io.Writer) error {
	raw, e := io.ReadAll(io.LimitReader(input, (64<<10)+1))
	if e != nil {
		return ErrInvalid
	}
	var task Task
	if StrictDecode(raw, &task) != nil || task.Validate() != nil {
		return ErrInvalid
	}
	ctx, cancel, e := elapsedDeadline(ctx, time.Duration(task.WallBudgetMS)*time.Millisecond)
	if e != nil {
		return e
	}
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	go func() {
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-tick.C:
				ms, e := processCPU()
				if e != nil || ms >= int64(task.CPUReservationMS)-150 {
					cancel()
					return
				}
			}
		}
	}()
	candidate, e := Search(ctx, task.Snapshot, task.First, task.Count)
	status := "ok"
	if e != nil {
		if !errors.Is(e, context.Canceled) && !errors.Is(e, context.DeadlineExceeded) {
			return e
		}
		status = "cancelled"
	}
	if candidate.ID == 0 && e == nil {
		status = "no_improvement"
	}
	used, e := processCPU()
	if e != nil || used > int64(task.CPUReservationMS) {
		return ErrBudget
	}
	result := Result{Version: 1, OwnerEpoch: task.OwnerEpoch, Task: task.ID, Attempt: task.Attempt, Session: task.Session, SnapshotDigest: task.SnapshotDigest, Status: status, Candidate: candidate, CPUUsedMS: used}
	return json.NewEncoder(output).Encode(result)
}

// Unlike the Go timer on some hosts, boot elapsed includes machine suspend.
// On resume the next watchdog tick cancels the child before more useful work.
func elapsedDeadline(parent context.Context, limit time.Duration) (context.Context, context.CancelFunc, error) {
	start, e := bootElapsed()
	if e != nil {
		return nil, nil, e
	}
	ctx, cancel := context.WithTimeout(parent, limit)
	go func() {
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				now, e := bootElapsed()
				if e != nil || now < start || now-start >= limit {
					cancel()
					return
				}
			}
		}
	}()
	return ctx, cancel, nil
}
