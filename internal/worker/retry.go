package worker

import "time"

// Desired configuration reconciliation is idempotent; maintenance commands
// remain governed by the existing consumed-command journal and are not replayed.
func (a *Agent) configurationFailed(generation int64) {
	previous := a.failed.Swap(generation)
	if previous != generation {
		a.failureAttempts.Store(0)
	}
	attempt := a.failureAttempts.Add(1)
	delay := time.Duration(min(int64(300), 5<<min(attempt-1, 6))) * time.Second
	a.retryAt.Store(time.Now().Add(delay).Unix())
}

func (a *Agent) mayRetryConfiguration(generation int64, now time.Time) bool {
	return a.failed.Load() != generation || now.Unix() >= a.retryAt.Load()
}
