package worker

import (
	"context"
	"io"
	"net"
	"proxyplatform/internal/core"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type heldEchoPackets struct {
	packets chan core.Packet
	done    chan struct{}
	once    sync.Once
}

func (p *heldEchoPackets) WritePacket(packet core.Packet) error {
	packet.Payload = append([]byte(nil), packet.Payload...)
	select {
	case p.packets <- packet:
		return nil
	case <-p.done:
		return net.ErrClosed
	}
}
func (p *heldEchoPackets) ReadPacket(buf []byte) (core.Packet, error) {
	select {
	case packet := <-p.packets:
		n := copy(buf, packet.Payload)
		packet.Payload = buf[:n]
		return packet, nil
	case <-p.done:
		return core.Packet{}, net.ErrClosed
	}
}
func (p *heldEchoPackets) SetReadDeadline(time.Time) error { return nil }
func (p *heldEchoPackets) Done() <-chan struct{}           { return p.done }
func (p *heldEchoPackets) Close() error                    { p.once.Do(func() { close(p.done) }); return nil }

func TestRolloverHeldOwnerContinuesWithoutMainLoopAndReportsFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client, server := net.Pipe()
	defer server.Close()
	go func() { defer server.Close(); io.Copy(server, server) }()
	packets := &heldEchoPackets{packets: make(chan core.Packet, 1), done: make(chan struct{})}
	var pid atomic.Int64
	pid.Store(12)
	owner := startRolloverHeldOwner(ctx, cancel, client, packets, "origin.test", 53, 12, pid.Load, func() (string, bool) { return "127.0.0.1:3000", false }, 20*time.Millisecond)
	defer func() {
		cancel()
		client.Close()
		packets.Close()
		select {
		case <-owner.Done:
		case <-time.After(time.Second):
			t.Error("held owner did not stop")
		}
	}()
	select {
	case <-owner.Ready:
	case err := <-owner.Errors:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("owner failed to start")
	}
	before := *owner.Snapshot.Load()
	// Main/control/checkpoint does no work during this interval. The dedicated
	// client still advances the same connection/association sequence.
	time.Sleep(120 * time.Millisecond)
	after := owner.Snapshot.Load()
	if after.TCPSequence <= before.TCPSequence || after.UDPSequence <= before.UDPSequence || !after.At.After(before.At) || after.OldPID != 12 || after.UDPOriginPeer != "127.0.0.1:3000" {
		t.Fatal("main-loop stall stopped held traffic", before, *after)
	}
	pid.Store(13)
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("held failure did not cancel control context")
	}
	select {
	case err := <-owner.Errors:
		if !strings.Contains(err.Error(), "held_engine_process_changed") {
			t.Fatal(err)
		}
	default:
		t.Fatal("failure not delivered to test goroutine")
	}
}
