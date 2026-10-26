package worker

import (
	"golang.org/x/sys/unix"
	"net"
	"proxyplatform/internal/topology"
	"sync"
	"sync/atomic"
	"time"
)

// Linux TCP state values used by TCP_INFO (include/net/tcp_states.h).
const (
	tcpTimeWait  = 6
	tcpCloseWait = 8
	tcpLastAck   = 9
	tcpClosing   = 11
)

// Embedding TCPConn retains its ReadFrom/WriteTo methods and Linux splice path.
// Only socket lifetime is intercepted; payload bytes do not pass through a Go wrapper.
type routeConn struct {
	ticket *routeTicket
	*net.TCPConn
	route              *Route
	once               sync.Once
	mu                 sync.Mutex
	gateway            *Gateway
	up                 uint64
	down               uint64
	err                error
	release            func()
	windowStart        time.Time
	windowDown         uint64
	receiveWindow      uint32
	openedAt           time.Time
	initialUp          uint64
	initialDown        uint64
	baselineValid      bool
	peerFIN            bool
	topologyGeneration int64
	topologyPolicy     string
	topologyListener   string
	topologyProtocol   string
	topologyFlow       string
	topologyEngine     string
	topologyEarly      atomic.Bool
	lastTransfer       time.Time
	idleObserved       bool
	closed             bool
}

func (c *routeConn) checkEarlyFailure() {
	c.sample()
	c.mu.Lock()
	suspect := c.baselineValid && time.Since(c.openedAt) <= 5*time.Second && c.up > c.initialUp && c.down == c.initialDown
	c.mu.Unlock()
	if suspect && c.route.Instance != nil {
		if c.gateway != nil && c.gateway.Topology != nil && c.topologyEarly.CompareAndSwap(false, true) {
			c.gateway.Topology.Offer(topology.Decision{Generation: c.topologyGeneration, Policy: c.topologyPolicy, Listener: c.topologyListener, Protocol: c.topologyProtocol, FlowID: c.topologyFlow, Attempted: c.route.Config.ID, Engine: c.topologyEngine, Reason: "early_transport_close", Method: "tcp_info_no_response_before_early_close", Truncated: true})
		}
		c.route.Instance.RequestHealthCheck()
	}
}

func (c *routeConn) Close() error {
	c.once.Do(func() {
		c.checkEarlyFailure()
		c.err = c.TCPConn.Close()
		c.mu.Lock()
		c.closed = true
		if c.idleObserved {
			c.route.IdleTCP.Add(-1)
			c.idleObserved = false
		}
		c.mu.Unlock()
		c.gateway.socketMu.Lock()
		delete(c.gateway.sockets, c)
		c.gateway.socketMu.Unlock()
		c.route.Active.Add(-1)
		c.mu.Lock()
		delivered := c.down > c.initialDown
		c.mu.Unlock()
		c.ticket.outcome(delivered)
		c.ticket.release()
		if c.release != nil {
			c.release()
		}
	})
	return c.err
}
func (c *routeConn) info() (uint64, uint64, bool) {
	raw, e := c.SyscallConn()
	if e != nil {
		return 0, 0, false
	}
	var info *unix.TCPInfo
	err := raw.Control(func(fd uintptr) { info, e = unix.GetsockoptTCPInfo(int(fd), unix.IPPROTO_TCP, unix.TCP_INFO) })
	if err != nil || e != nil || info == nil {
		return 0, 0, false
	}
	up := uint64(0)
	if info.Bytes_sent >= info.Bytes_retrans {
		up = info.Bytes_sent - info.Bytes_retrans
	}
	c.receiveWindow = info.Rcv_wnd
	// Linux advances bytes_received for the peer's FIN sequence number too.
	// Observe it before the socket reaches CLOSE and retain that observation;
	// a FIN is neither response payload nor evidence that an upstream worked.
	switch info.State {
	case tcpCloseWait, tcpClosing, tcpLastAck, tcpTimeWait:
		c.peerFIN = true
	}
	down := info.Bytes_received
	if c.peerFIN && down > 0 {
		down--
	}
	return up, down, true
}
func (c *routeConn) baseline() {
	c.up, c.down, c.baselineValid = c.info()
	c.initialUp, c.initialDown = c.up, c.down
	c.openedAt = time.Now()
	c.windowStart = time.Now()
	c.windowDown = c.down
}
func (c *routeConn) sample() {
	c.mu.Lock()
	defer c.mu.Unlock()
	up, down, ok := c.info()
	if !ok {
		return
	}
	if up > c.up || down > c.down {
		c.lastTransfer = time.Now()
	}
	if up >= c.up {
		c.route.BytesUp.Add(int64(up - c.up))
	}
	if down >= c.down {
		c.route.BytesDown.Add(int64(down - c.down))
	}
	c.up, c.down = up, down
	window := time.Since(c.windowStart)
	if window >= 250*time.Millisecond && down >= c.windowDown && down-c.windowDown >= 1<<20 {
		bps := int64(float64(down-c.windowDown) / window.Seconds())
		c.route.recordDeliveredRate(bps, c.receiveWindow, time.Now())
		c.windowStart = time.Now()
		c.windowDown = down
	} else if window > time.Minute {
		c.windowStart = time.Now()
		c.windowDown = down
	}
}

// Flush kernel counters at both boundaries of an active capacity sample.
// The regular one-second sampler is too coarse for a short bounded transfer.
// Payload forwarding retains its splice path; this runs only in the prober.
func (g *Gateway) routeDownSnapshot(route *Route) int64 {
	if g != nil {
		g.socketMu.Lock()
		list := make([]*routeConn, 0)
		for c := range g.sockets {
			if c.route == route {
				list = append(list, c)
			}
		}
		g.socketMu.Unlock()
		for _, c := range list {
			c.sample()
		}
	}
	return route.BytesDown.Load()
}

func (g *Gateway) sampleSockets() {
	type observation struct {
		up, down int64
		downBPS  int64
		at       time.Time
	}
	observed := map[*Route]observation{}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-g.ctx.Done():
			return
		case <-ticker.C:
			g.socketMu.Lock()
			list := make([]*routeConn, 0, len(g.sockets))
			for c := range g.sockets {
				list = append(list, c)
			}
			g.socketMu.Unlock()
			for _, c := range list {
				if c.ticket != nil && !c.ticket.allowed() {
					c.Close()
					continue
				}
				c.sample()
				c.observeIdle(time.Now())
			}
			routes := []*Route{}
			if g.Dynamic != nil {
				g.Dynamic.mu.RLock()
				for _, e := range g.Dynamic.entries {
					routes = append(routes, e.route)
				}
				g.Dynamic.mu.RUnlock()
			} else {
				routes = g.Routes
			}
			keep := map[*Route]bool{}
			now := time.Now()
			for _, v := range routes {
				v.WorkloadAt.Store(now.Unix())
				up, down := v.BytesUp.Load(), v.BytesDown.Load()
				previous, ok := observed[v]
				var downBPS int64
				if ok {
					seconds := now.Sub(previous.at).Seconds()
					if seconds > 0 {
						v.TrafficUpBPS.Store(int64(float64(max(int64(0), up-previous.up)) / seconds))
						downBPS = int64(float64(max(int64(0), down-previous.down)) / seconds)
						v.TrafficDownBPS.Store(downBPS)
						v.TrafficAt.Store(now.Unix())
						if seconds <= 3 {
							v.delivery.observe(downBPS, v.TrafficUpBPS.Load(), now)
							v.requestOnAggregateDrop(previous.downBPS, downBPS, now)
						} else {
							// A stalled observer is not fresh routing evidence.
							v.TrafficAt.Store(0)
							downBPS = 0
						}
					}
				}
				observed[v] = observation{up: up, down: down, downBPS: downBPS, at: now}
				keep[v] = true
			}
			for v := range observed {
				if !keep[v] {
					delete(observed, v)
				}
			}
		}
	}
}

func (c *routeConn) observeIdle(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// New sockets remain fully charged during connection/TLS setup.
	idle := !c.closed && c.baselineValid && now.Sub(c.openedAt) >= 3*time.Second &&
		(c.lastTransfer.IsZero() || now.Sub(c.lastTransfer) >= 3*time.Second)
	if idle != c.idleObserved {
		if idle {
			c.route.IdleTCP.Add(1)
		} else {
			c.route.IdleTCP.Add(-1)
		}
		c.idleObserved = idle
	}
}
