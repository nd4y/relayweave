package control

import (
	"proxyplatform/internal/wire"
	"testing"
)

func TestCommandsAndResultsBelongToOneWorker(t *testing.T) {
	for _, c := range []wire.Command{{}, {WorkerID: "worker-1"}} {
		if !commandForWorker(c, "worker-1") || commandForWorker(c, "worker-2") {
			t.Fatal("legacy or primary command leaked to another worker")
		}
	}
	c := wire.Command{WorkerID: "worker-2"}
	if commandForWorker(c, "worker-1") || !commandForWorker(c, "worker-2") {
		t.Fatal("targeted command routed incorrectly")
	}
}
