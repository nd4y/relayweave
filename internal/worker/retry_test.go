package worker

import (
	"testing"
	"time"
)

func TestConfigurationFailureRetriesWithoutReplayingCommand(t *testing.T) {
	a := &Agent{}
	a.configurationFailed(4)
	if a.mayRetryConfiguration(4, time.Now()) {
		t.Fatal("retry loop unbounded")
	}
	if !a.mayRetryConfiguration(4, time.Now().Add(6*time.Second)) || !a.mayRetryConfiguration(5, time.Now()) {
		t.Fatal("autonomous recovery stuck on failed generation")
	}
	for range 100 {
		a.configurationFailed(4)
	}
	if a.retryAt.Load() > time.Now().Add(301*time.Second).Unix() {
		t.Fatal("retry ceiling exceeded")
	}
}
