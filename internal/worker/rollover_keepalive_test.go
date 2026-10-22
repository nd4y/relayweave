package worker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"proxyplatform/internal/core"
	"strconv"
	"sync/atomic"
	"time"
)

type rolloverHeldSnapshot struct {
	TCPSequence, UDPSequence uint64
	At                       time.Time
	OldPID                   int64
	UDPOriginPeer            string
}

type rolloverHeldOwner struct {
	Snapshot    atomic.Pointer[rolloverHeldSnapshot]
	Errors      chan error
	Ready, Done chan struct{}
}

// One owner alone reads/writes the original held TCP and UDP association.
// Journal/checkpoint fsync, config Apply and probes run independently. No new
// socket can substitute for either held connection, and no testing.T method is
// called in this goroutine. Main owns failure reporting and orderly shutdown.
func startRolloverHeldOwner(ctx context.Context, cancelOnError context.CancelFunc, tcp net.Conn, udp core.PacketSession, target string, port int, oldPID int64, currentPID func() int64, peer func() (string, bool), interval time.Duration) *rolloverHeldOwner {
	o := &rolloverHeldOwner{Errors: make(chan error, 1), Ready: make(chan struct{}), Done: make(chan struct{})}
	go func() {
		defer close(o.Done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		fail := func(err error) {
			if ctx.Err() == nil {
				o.Errors <- err
				cancelOnError()
			}
		}
		var sequence uint64
		for {
			if ctx.Err() != nil {
				return
			}
			if currentPID() != oldPID {
				fail(fmt.Errorf("held_engine_process_changed"))
				return
			}
			sequence++
			if err := rolloverEchoSequence(tcp, sequence); err != nil {
				fail(err)
				return
			}
			if err := udp.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
				fail(err)
				return
			}
			payload := []byte("persistent-udp-sequence-" + strconv.FormatUint(sequence, 10))
			if err := udp.WritePacket(core.Packet{Host: target, Port: port, Payload: payload}); err != nil {
				fail(err)
				return
			}
			packet, err := udp.ReadPacket(make([]byte, 2048))
			if err != nil {
				fail(fmt.Errorf("held_udp_interrupted: %w", err))
				return
			}
			if !bytes.Equal(packet.Payload, payload) {
				fail(fmt.Errorf("held_udp_payload_mismatch"))
				return
			}
			actualPeer, changed := peer()
			if changed || actualPeer == "" {
				fail(fmt.Errorf("held_udp_origin_mapping_changed_or_unobserved"))
				return
			}
			if currentPID() != oldPID {
				fail(fmt.Errorf("held_engine_process_changed"))
				return
			}
			o.Snapshot.Store(&rolloverHeldSnapshot{TCPSequence: sequence, UDPSequence: sequence, At: time.Now(), OldPID: oldPID, UDPOriginPeer: actualPeer})
			if sequence == 1 {
				close(o.Ready)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return o
}

func rolloverEchoSequence(conn net.Conn, sequence uint64) error {
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return err
	}
	payload := []byte("persistent-sequence-payload-" + strconv.FormatUint(sequence, 10))
	if n, err := conn.Write(payload); err != nil {
		return err
	} else if n != len(payload) {
		return io.ErrShortWrite
	}
	got := make([]byte, len(payload))
	_, err := io.ReadFull(conn, got)
	if err != nil {
		return fmt.Errorf("held_tcp_interrupted: %w", err)
	}
	if !bytes.Equal(got, payload) {
		return fmt.Errorf("held_tcp_payload_mismatch")
	}
	return nil
}
