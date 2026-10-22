package worker

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"proxyplatform/internal/config"
	"proxyplatform/internal/core"
	"proxyplatform/internal/routing"
	"proxyplatform/internal/socks"
	"proxyplatform/internal/topology"
)

type Route struct {
	IdleTCP             atomic.Int64
	WorkloadAt          atomic.Int64
	delivery            deliveryWindow
	FailureProbeSamples atomic.Int64
	TrafficDownBPS      atomic.Int64
	TrafficUpBPS        atomic.Int64
	TrafficAt           atomic.Int64
	UDPHealthy          atomic.Bool
	UDPAttempt          atomic.Int64
	UDPChecked          atomic.Int64
	UDPChecks           atomic.Int64
	References          atomic.Int64
	localStats          routing.Estimator
	localRate           routing.RateEstimator
	localEnabled        atomic.Bool
	Config              config.Route
	Instance            *core.Instance
	Connections         atomic.Int64
	Failures            atomic.Int64
	Active              atomic.Int64
	BytesUp             atomic.Int64
	BytesDown           atomic.Int64
	RTTUS               atomic.Int64
	Samples             atomic.Int64
	ConsecutiveFailures atomic.Int64
	BlockedUntil        atomic.Int64
	SpeedBPS            atomic.Int64
	LastSpeed           atomic.Int64
	CapacityConnections atomic.Int64
	ErrorEWMA           atomic.Int64
	ProbeRequestedAt    atomic.Int64
	ProbeNotBefore      atomic.Int64
	LastProbeAttempt    atomic.Int64
	PassiveBPS          atomic.Int64
	LastPassive         atomic.Int64
	CeilingBPS          int64
}
type Gateway struct {
	Topology     *topology.Observer
	Config       config.Config
	Manager      *core.Manager
	Routes       []*Route
	next         atomic.Uint64
	active       atomic.Int64
	udpActive    atomic.Int64
	udpDrops     atomic.Int64
	udpPackets   atomic.Int64
	ctx          context.Context
	mu           sync.Mutex
	connections  map[net.Conn]struct{}
	wg           sync.WaitGroup
	listeners    []net.Listener
	servers      []*http.Server
	transports   []*http.Transport
	portMu       sync.Mutex
	ports        map[int]bool
	Dynamic      *Runtime
	listenerMu   sync.Mutex
	bound        map[string]boundListener
	connects     connectStats
	socketMu     sync.Mutex
	sockets      map[*routeConn]struct{}
	destMu       sync.Mutex
	destFailures map[[32]byte]map[string]time.Time
}
type boundListener struct {
	config    config.Listener
	listener  net.Listener
	server    *http.Server
	transport *http.Transport
}

func (b boundListener) retire() {
	b.listener.Close()
	if b.server != nil {
		b.server.SetKeepAlivesEnabled(false)
	}
	if b.transport != nil {
		b.transport.CloseIdleConnections()
	}
}

func (g *Gateway) configuration() config.Config {
	if g.Dynamic != nil {
		return g.Dynamic.Configuration()
	}
	return g.Config
}
func (g *Gateway) effective(l config.Listener, remote string) (config.Listener, bool) {
	if g.Dynamic != nil {
		var ok bool
		l, ok = g.Dynamic.Listener(l.ID)
		if !ok {
			return l, false
		}
	}
	host, _, e := net.SplitHostPort(remote)
	ip := net.ParseIP(host)
	if e != nil || ip == nil {
		return l, false
	}
	if len(l.AllowCIDRs) == 0 {
		return l, true
	}
	for _, cidr := range l.AllowCIDRs {
		_, n, e := net.ParseCIDR(cidr)
		if e == nil && n.Contains(ip) {
			return l, true
		}
	}
	return l, false
}
func routeKey(l config.Listener) string {
	if l.Policy != "" {
		return "policy:" + l.Policy
	}
	return l.Route
}

func New(ctx context.Context, c config.Config, m *core.Manager) *Gateway {
	g := &Gateway{Topology: topology.NewObserver(), Config: c, Manager: m, ctx: ctx, connections: map[net.Conn]struct{}{}, ports: map[int]bool{}, sockets: map[*routeConn]struct{}{}}
	go g.sampleSockets()
	for _, r := range c.Routes {
		g.Routes = append(g.Routes, &Route{Config: r, Instance: m.Instances[r.Core]})
	}
	return g
}
func (g *Gateway) candidates(pin string) []*Route {
	if g.Dynamic != nil {
		return g.Dynamic.Candidates(pin)
	}
	var all []*Route
	for _, r := range g.Routes {
		if (pin == "" || pin == r.Config.ID) && r.Instance.Ready.Load() {
			all = append(all, r)
		}
	}
	if len(all) > 1 {
		start := int(g.next.Add(1)-1) % len(all)
		all = append(all[start:], all[:start]...)
	}
	return all
}
func (g *Gateway) dial(ctx context.Context, pin, target string) (net.Conn, *Route, error) {
	return g.dialObserved(ctx, pin, target, "unknown", "tcp")
}
func (g *Gateway) dialObserved(ctx context.Context, pin, target, listener, protocol string) (net.Conn, *Route, error) {
	if forbiddenTarget(target) {
		return nil, nil, errors.New("destination_denied")
	}
	release := func() {}
	if g.Dynamic != nil {
		var e error
		release, e = g.Dynamic.AdmitPolicy(pin)
		if e != nil {
			reason := "insufficient_observation"
			if e.Error() == "policy_connection_budget_exhausted" {
				reason = "max_connections"
			}
			g.topologyResult(&topology.Decision{Generation: g.configuration().Generation, Policy: strings.TrimPrefix(pin, "policy:"), Listener: listener, Protocol: protocol, Method: "admission_constraint"}, "", reason)
			return nil, nil, e
		}
	}
	committed := false
	defer func() {
		if !committed {
			release()
		}
	}()
	startedAll := time.Now()
	defer func() { g.connects.observe(time.Since(startedAll)) }()
	ctx, cancel := context.WithTimeout(ctx, g.configuration().Timeout()*3)
	defer cancel()
	var last error = errors.New("no ready route")
	attempts := 0
	candidates, decision := g.topologyCandidates(pin, listener, protocol)
	for _, r := range candidates {
		if g.targetCooling(target, r.Config.ID) {
			g.topologyRouteResult(decision, r, "target_cooldown")
			continue
		}
		if attempts >= 3 {
			break
		}
		attempts++
		g.connects.attempts.Add(1)
		var ticket *routeTicket
		if g.Dynamic != nil {
			var err error
			ticket, err = g.Dynamic.reserveRoute(pin, r)
			if err != nil {
				continue
			}
			r = ticket.route
			if scope, ok := ctx.Value(listenerScopeKey{}).(listenerScope); ok {
				ticket.scope = &scope
			}
		}
		started := time.Now()
		c, e := r.Instance.Adapter().OpenTCP(ctx, r.Config.Endpoint, target, g.configuration().Timeout())
		if e == nil {
			if ticket != nil && !ticket.allowed() {
				c.Close()
				ticket.release()
				continue
			}
			r.Connections.Add(1)
			r.success(time.Since(started))
			if tcp, ok := c.(*net.TCPConn); ok {
				r.Active.Add(1)
				tracked := &routeConn{TCPConn: tcp, route: r, gateway: g, release: release}
				tracked.ticket = ticket
				if g.Topology != nil {
					tracked.topologyGeneration = g.configuration().Generation
					tracked.topologyEngine = r.Config.Core
					if g.Dynamic != nil {
						tracked.topologyGeneration, tracked.topologyEngine = g.Dynamic.topologyIdentity(r)
					}
					tracked.topologyPolicy = strings.TrimPrefix(pin, "policy:")
					tracked.topologyListener = listener
					tracked.topologyProtocol = protocol
					if decision != nil {
						tracked.topologyGeneration = decision.Generation
						tracked.topologyFlow = decision.FlowID
					}
				}
				tracked.baseline()
				g.socketMu.Lock()
				g.sockets[tracked] = struct{}{}
				g.socketMu.Unlock()
				c = tracked
				committed = true
			}
			if !committed && ticket != nil {
				c.Close()
				ticket.release()
				continue
			}
			g.topologyRouteResult(decision, r, "selected_by_policy")
			return c, r, nil
		}
		// Xray can reject the upstream before its private SOCKS handshake
		if ticket != nil {
			ticket.outcome(false)
			ticket.release()
		}
		// succeeds, so no tracked tunnel exists to observe a later EOF.
		// Confirm the proxy independently; the destination may be at fault.
		r.Instance.RequestHealthCheck()
		r.Failures.Add(1)
		g.connects.failures.Add(1)
		r.ErrorEWMA.Store((r.ErrorEWMA.Load()*7 + 1000) / 8)
		g.targetFailed(target, r.Config.ID)
		var reply socks.ReplyError
		if !r.Instance.Ready.Load() || (!errors.As(e, &reply) && !isTimeout(e)) {
			r.failed()
		}
		last = e
		g.topologyRouteResult(decision, r, "target_connect_failed")
	}
	if attempts == 0 {
		reason := "insufficient_observation"
		if decision != nil && decision.Reason != "selected_by_policy" {
			reason = decision.Reason
		}
		g.topologyResult(decision, "", reason)
	}
	return nil, nil, last
}
func (g *Gateway) admit(c net.Conn) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ctx.Err() != nil || g.active.Load() >= int64(g.configuration().MaxConnections) {
		return false
	}
	g.connections[c] = struct{}{}
	g.active.Add(1)
	return true
}
func (g *Gateway) release(c net.Conn) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.connections[c]; ok {
		delete(g.connections, c)
		g.active.Add(-1)
	}
}
func auth(l config.Listener, user, pass string) bool {
	if strings.HasPrefix(l.Password, "$2") {
		return hashedAuth(l, user, pass)
	}
	a := sha256.Sum256([]byte(user))
	b := sha256.Sum256([]byte(l.Username))
	c := sha256.Sum256([]byte(pass))
	d := sha256.Sum256([]byte(l.Password))
	return subtle.ConstantTimeCompare(a[:], b[:])&subtle.ConstantTimeCompare(c[:], d[:]) == 1
}
func (g *Gateway) Start() error {
	if e := g.Reconfigure(g.configuration()); e != nil {
		return e
	}
	return g.startMetrics()
}
func (g *Gateway) Reconfigure(c config.Config) error {
	g.listenerMu.Lock()
	defer g.listenerMu.Unlock()
	if g.bound == nil {
		g.bound = map[string]boundListener{}
	}
	opened := map[string]net.Listener{}
	for _, l := range c.Listeners {
		if old, ok := g.bound[l.ID]; ok && old.config.Listen == l.Listen && old.config.Protocol == l.Protocol {
			continue
		}
		for _, old := range g.bound {
			if old.config.Listen == l.Listen {
				for _, v := range opened {
					v.Close()
				}
				return fmt.Errorf("listener address is already bound; change ID or protocol with a new port")
			}
		}
		ln, e := net.Listen("tcp", l.Listen)
		if e != nil {
			for _, v := range opened {
				v.Close()
			}
			return e
		}
		opened[l.ID] = ln
	}
	wanted := map[string]bool{}
	for _, l := range c.Listeners {
		wanted[l.ID] = true
		if ln, ok := opened[l.ID]; ok {
			binding := g.openListener(l, ln)
			if old, exists := g.bound[l.ID]; exists {
				old.retire()
			}
			g.bound[l.ID] = binding
		}
	}
	for id, v := range g.bound {
		if !wanted[id] {
			v.retire()
			delete(g.bound, id)
		}
	}
	return nil
}
func (g *Gateway) openListener(l config.Listener, ln net.Listener) boundListener {
	binding := boundListener{config: l, listener: ln}
	if l.Protocol == "http" {
		tr := &http.Transport{Proxy: nil, MaxIdleConns: 64, MaxIdleConnsPerHost: 16, IdleConnTimeout: 30 * time.Second, ResponseHeaderTimeout: 15 * time.Second, DisableCompression: true, DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			current := l
			if g.Dynamic != nil {
				var ok bool
				current, ok = g.Dynamic.Listener(l.ID)
				if !ok {
					return nil, fmt.Errorf("listener removed")
				}
			}
			c, _, e := g.dialObserved(ctx, routeKey(current), addr, current.ID, current.Protocol)
			return c, e
		}}
		binding.transport = tr
		srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { g.httpProxy(l, tr, w, r) }), ConnState: func(c net.Conn, s http.ConnState) {
			if s == http.StateNew {
				if !g.admit(c) {
					c.Close()
				}
			} else if s == http.StateClosed {
				g.release(c)
			}
		}}
		binding.server = srv
		g.wg.Add(1)
		go func() {
			defer g.wg.Done()
			if e := srv.Serve(ln); e != nil && !errors.Is(e, http.ErrServerClosed) {
				slog.Debug("HTTP listener stopped", "error", e)
			}
		}()
	} else {
		g.wg.Add(1)
		go func() { defer g.wg.Done(); g.serveSOCKS(l, ln) }()
	}
	return binding
}
func (g *Gateway) startMetrics() error {
	ln, e := net.Listen("tcp", g.configuration().MetricsListen)
	if e != nil {
		g.closeListeners()
		return e
	}
	g.listeners = append(g.listeners, ln)
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if len(g.candidates("")) == 0 {
			http.Error(w, "no ready routes", 503)
			return
		}
		w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/status", g.status)
	mux.HandleFunc("/livez", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	mux.HandleFunc("/metrics", g.metrics)
	handler, err := metricsAccess(mux)
	if err != nil {
		ln.Close()
		return err
	}
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	g.servers = append(g.servers, srv)
	g.wg.Add(1)
	go func() { defer g.wg.Done(); srv.Serve(ln) }()
	return nil
}
func (g *Gateway) closeListeners() {
	g.listenerMu.Lock()
	defer g.listenerMu.Unlock()
	for id, b := range g.bound {
		b.retire()
		if b.server != nil {
			b.server.Close()
		}
		delete(g.bound, id)
	}
	for _, l := range g.listeners {
		l.Close()
	}
	for _, s := range g.servers {
		s.Close()
	}
	for _, t := range g.transports {
		t.CloseIdleConnections()
	}
}
func (g *Gateway) Close() {
	g.closeListeners()
	g.mu.Lock()
	for c := range g.connections {
		c.Close()
	}
	g.mu.Unlock()
	g.wg.Wait()
}
func (g *Gateway) serveSOCKS(l config.Listener, ln net.Listener) {
	for {
		c, e := ln.Accept()
		if e != nil {
			return
		}
		current, allowed := g.effective(l, c.RemoteAddr().String())
		if !allowed {
			c.Close()
			continue
		}
		if !g.admit(c) {
			c.Close()
			continue
		}
		g.wg.Add(1)
		go func() { defer g.wg.Done(); defer g.release(c); defer c.Close(); g.handleSOCKS(current, c) }()
	}
}
func (g *Gateway) handleSOCKS(l config.Listener, c net.Conn) {
	authGeneration := sha256.Sum256([]byte(l.Username + "\x00" + l.Password))
	c.SetDeadline(time.Now().Add(10 * time.Second))
	var h [2]byte
	if _, e := io.ReadFull(c, h[:]); e != nil || h[0] != 5 || h[1] == 0 {
		return
	}
	methods := make([]byte, int(h[1]))
	if _, e := io.ReadFull(c, methods); e != nil {
		return
	}
	method := byte(0)
	if l.Username != "" {
		method = 2
	}
	supported := false
	for _, m := range methods {
		if m == method {
			supported = true
		}
	}
	if !supported {
		socks.Write(c, []byte{5, 255})
		return
	}
	if socks.Write(c, []byte{5, method}) != nil {
		return
	}
	if method == 2 {
		if _, e := io.ReadFull(c, h[:]); e != nil || h[0] != 1 || h[1] == 0 {
			return
		}
		user := make([]byte, int(h[1]))
		if _, e := io.ReadFull(c, user); e != nil {
			return
		}
		var plen [1]byte
		if _, e := io.ReadFull(c, plen[:]); e != nil || plen[0] == 0 {
			return
		}
		pass := make([]byte, int(plen[0]))
		if _, e := io.ReadFull(c, pass); e != nil {
			return
		}
		if !auth(l, string(user), string(pass)) {
			socks.Write(c, []byte{1, 1})
			return
		}
		if socks.Write(c, []byte{1, 0}) != nil {
			return
		}
	}
	var req [3]byte
	if _, e := io.ReadFull(c, req[:]); e != nil || req[0] != 5 || req[2] != 0 {
		return
	}
	host, port, _, e := socks.ReadAddress(c)
	if e != nil {
		socks.Reply(c, 8, "0.0.0.0", 0)
		return
	}
	current, allowed := g.effective(l, c.RemoteAddr().String())
	if !allowed || sha256.Sum256([]byte(current.Username+"\x00"+current.Password)) != authGeneration {
		socks.Reply(c, 2, "0.0.0.0", 0)
		return
	}
	l = current
	switch req[1] {
	case 1:
		if port == 0 {
			socks.Reply(c, 1, "0.0.0.0", 0)
			return
		}
		up, r, e := g.dialObserved(scopedContext(g.ctx, l, c.RemoteAddr().String()), routeKey(l), net.JoinHostPort(host, strconv.Itoa(port)), l.ID, "socks5-tcp")
		if e != nil {
			socks.Reply(c, 4, "0.0.0.0", 0)
			return
		}
		defer up.Close()
		if socks.Reply(c, 0, "0.0.0.0", 0) != nil {
			return
		}
		c.SetDeadline(time.Time{})
		g.tunnel(c, up, r)
	case 3:
		if !l.UDP {
			socks.Reply(c, 7, "0.0.0.0", 0)
			return
		}
		c.SetDeadline(time.Time{})
		g.udpAssociate(l, c, host, port)
	default:
		socks.Reply(c, 7, "0.0.0.0", 0)
	}
}
func (g *Gateway) tunnel(client, up net.Conn, r *Route) {
	stop := context.AfterFunc(g.ctx, func() { up.Close(); client.Close() })
	defer stop()
	done := make(chan struct{})
	go func() {
		defer close(done)
		io.Copy(up, client)
		if t, ok := up.(interface{ CloseWrite() error }); ok {
			t.CloseWrite()
		} else {
			up.Close()
		}
	}()
	io.Copy(client, up)
	if tracked, ok := up.(*routeConn); ok {
		// Inspect the peer EOF before half-closing the client or waiting for
		// its upload side. Keep io.Copy's TCP splice path intact.
		tracked.checkEarlyFailure()
	}
	if t, ok := client.(*net.TCPConn); ok {
		t.CloseWrite()
	}
	<-done
}
func stripHop(h http.Header) {
	for _, v := range h.Values("Connection") {
		for _, name := range strings.Split(v, ",") {
			h.Del(strings.TrimSpace(name))
		}
	}
	for _, k := range []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "TE", "Trailer", "Transfer-Encoding", "Upgrade"} {
		h.Del(k)
	}
}
func (g *Gateway) httpProxy(l config.Listener, tr *http.Transport, w http.ResponseWriter, r *http.Request) {
	var allowed bool
	l, allowed = g.effective(l, r.RemoteAddr)
	r = r.WithContext(scopedContext(r.Context(), l, r.RemoteAddr))
	if !allowed {
		http.Error(w, "listener unavailable or client denied", 403)
		return
	}
	if l.Username != "" {
		token := r.Header.Get("Proxy-Authorization")
		u, p := "", ""
		if strings.HasPrefix(token, "Basic ") {
			b, e := base64.StdEncoding.DecodeString(strings.TrimPrefix(token, "Basic "))
			if e == nil {
				u, p, _ = strings.Cut(string(b), ":")
			}
		}
		if !auth(l, u, p) {
			w.Header().Set("Proxy-Authenticate", `Basic realm="proxy-platform"`)
			http.Error(w, "proxy authentication required", 407)
			return
		}
	}
	if r.Method == http.MethodConnect {
		if _, _, e := net.SplitHostPort(r.Host); e != nil {
			http.Error(w, "CONNECT requires host:port", 400)
			return
		}
		up, route, e := g.dialObserved(r.Context(), routeKey(l), r.Host, l.ID, "http-connect")
		if e != nil {
			http.Error(w, "no usable upstream", 502)
			return
		}
		defer up.Close()
		h, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "hijacking unavailable", 500)
			return
		}
		c, buffer, e := h.Hijack()
		if e != nil {
			return
		}
		defer g.release(c)
		defer c.Close()
		c.SetDeadline(time.Time{})
		if _, e = buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); e != nil {
			return
		}
		if buffer.Flush() != nil {
			return
		}
		if n := buffer.Reader.Buffered(); n > 0 {
			if _, e = io.CopyN(up, buffer.Reader, int64(n)); e != nil {
				return
			}
		}
		g.tunnel(c, up, route)
		return
	}
	if r.URL.Scheme != "http" || r.URL.Host == "" {
		http.Error(w, "absolute HTTP proxy request required", 400)
		return
	}
	out := r.Clone(r.Context())
	out.RequestURI = ""
	stripHop(out.Header)
	if r.Close && (r.Method == http.MethodGet || r.Method == http.MethodHead) && (r.Body == nil || r.Body == http.NoBody) {
		g.singleRequestForward(l, w, r, out)
		return
	}
	if g.Dynamic != nil {
		key := routeKey(l)
		out = out.WithContext(httptrace.WithClientTrace(out.Context(), &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
			if c, ok := info.Conn.(*routeConn); ok && (!g.Dynamic.RouteAllowed(key, c.route.Config) || (c.ticket != nil && !g.Dynamic.canReuse(c.ticket))) {
				c.Close()
			}
		}}))
	}
	resp, e := tr.RoundTrip(out)
	if e != nil {
		http.Error(w, "upstream request failed", 502)
		return
	}
	defer resp.Body.Close()
	stripHop(resp.Header)
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// A single-request HTTP connection can stream a known-length body directly between TCP
// sockets after parsing headers. This keeps Linux splice available without per-byte wrappers.
// Keep-alive, uploads and chunked bodies continue through net/http's general transport.
func (g *Gateway) singleRequestForward(l config.Listener, w http.ResponseWriter, r, out *http.Request) {
	addr := out.URL.Host
	if _, _, e := net.SplitHostPort(addr); e != nil {
		addr = net.JoinHostPort(out.URL.Hostname(), "80")
	}
	up, _, e := g.dialObserved(r.Context(), routeKey(l), addr, l.ID, "http")
	if e != nil {
		http.Error(w, "no usable upstream", 502)
		return
	}
	defer up.Close()
	stop := context.AfterFunc(g.ctx, func() { up.Close() })
	defer stop()
	up.SetDeadline(time.Now().Add(15 * time.Second))
	out.Close = true
	if e = out.Write(up); e != nil {
		http.Error(w, "upstream request failed", 502)
		return
	}
	reader := bufio.NewReaderSize(up, 32<<10)
	resp, e := http.ReadResponse(reader, out)
	if e != nil {
		http.Error(w, "upstream response failed", 502)
		return
	}
	up.SetDeadline(time.Time{})
	if resp.ContentLength < 0 || len(resp.TransferEncoding) > 0 {
		defer resp.Body.Close()
		stripHop(resp.Header)
		for k, v := range resp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
		return
	}
	h, ok := w.(http.Hijacker)
	if !ok {
		resp.Body.Close()
		http.Error(w, "hijacking unavailable", 500)
		return
	}
	client, buffer, e := h.Hijack()
	if e != nil {
		return
	}
	defer client.Close()
	defer g.release(client)
	client.SetDeadline(time.Time{})
	stripHop(resp.Header)
	resp.Header.Set("Connection", "close")
	resp.Header.Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
	fmt.Fprintf(buffer, "HTTP/1.1 %s\r\n", resp.Status)
	resp.Header.Write(buffer)
	buffer.WriteString("\r\n")
	if buffer.Flush() != nil {
		return
	}
	if r.Method == http.MethodHead {
		return
	}
	left := resp.ContentLength
	if n := min(int64(reader.Buffered()), left); n > 0 {
		written, err := io.CopyN(client, reader, n)
		if err != nil {
			return
		}
		left -= written
	}
	if left > 0 {
		source := up
		if tracked, ok := up.(*routeConn); ok {
			source = tracked.TCPConn
		}
		io.CopyN(client, source, left)
	}
}
func (g *Gateway) status(w http.ResponseWriter, _ *http.Request) {
	if g.Dynamic != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(g.Dynamic.Status(g))
		return
	}
	cores := []map[string]any{}
	for _, i := range g.Manager.Instances {
		cores = append(cores, map[string]any{"id": i.Config.ID, "version": i.Version, "sha256": i.SHA256, "pid": i.PID.Load(), "ready": i.Ready.Load(), "starts": i.Starts.Load(), "restarts": i.Restarts.Load()})
	}
	routes := []map[string]any{}
	for _, r := range g.Routes {
		routes = append(routes, map[string]any{"id": r.Config.ID, "core": r.Config.Core, "connections": r.Connections.Load(), "failures": r.Failures.Load(), "active": r.Active.Load()})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"cores": cores, "routes": routes, "active_connections": g.active.Load(), "udp_associations": g.udpActive.Load(), "udp_drops": g.udpDrops.Load(), "udp_packets": g.udpPackets.Load()})
}
func (g *Gateway) metrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	g.Topology.Metrics(w)
	fmt.Fprintf(w, "proxy_connections_active %d\nproxy_udp_associations_active %d\nproxy_udp_drops_total %d\nproxy_udp_packets_total %d\n", g.active.Load(), g.udpActive.Load(), g.udpDrops.Load(), g.udpPackets.Load())
	g.connects.write(w)
	resourceMetrics(w)
	if g.Dynamic != nil {
		g.Dynamic.Metrics(w)
		return
	}
	for _, r := range g.Routes {
		fmt.Fprintf(w, "proxy_route_connections_total{route=%q,core=%q} %d\nproxy_route_failures_total{route=%q} %d\nproxy_traffic_bytes_total{route=%q,direction=\"up\"} %d\nproxy_traffic_bytes_total{route=%q,direction=\"down\"} %d\n", r.Config.ID, r.Config.Core, r.Connections.Load(), r.Config.ID, r.Failures.Load(), r.Config.ID, r.BytesUp.Load(), r.Config.ID, r.BytesDown.Load())
	}
	for _, i := range g.Manager.Instances {
		ready := 0
		if i.Ready.Load() {
			ready = 1
		}
		fmt.Fprintf(w, "proxy_core_ready{core=%q,version=%q} %d\nproxy_core_restarts_total{core=%q} %d\n", i.Config.ID, i.Config.Version, ready, i.Config.ID, i.Restarts.Load())
	}
}

// Health is implemented in the binary so a shell/curl is unnecessary in the runtime image.
func Health(url string) error {
	c := http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}}
	r, e := c.Get(url)
	if e != nil {
		return e
	}
	r.Body.Close()
	if r.StatusCode != 200 {
		return fmt.Errorf("health status %d", r.StatusCode)
	}
	return nil
}
