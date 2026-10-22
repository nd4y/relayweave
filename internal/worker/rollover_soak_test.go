package worker

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

type rolloverSoakCheckpoint struct {
	SourceSHA, CoreSHA, CoreVersion string
	HeldSince, At                   time.Time
	Seconds                         float64
	TCPSequence, UDPSequence        uint64
	OldPID, OldActive, References   int64
	Engines, Rotations              int
	Final, DR1Passed                bool
}

func rolloverSoakDuration(t *testing.T) time.Duration {
	t.Helper()
	raw := os.Getenv("ROLLOVER_SOAK_SECONDS")
	if raw == "" || raw == "0" {
		return 0
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 86400 || n > 26*3600 {
		t.Fatal("soak duration must be actual24h to26h; functional smoke uses0")
	}
	sha := os.Getenv("ROLLOVER_SOURCE_SHA")
	_, err = hex.DecodeString(sha)
	if err != nil || (len(sha) != 40 && len(sha) != 64) || os.Getenv("ROLLOVER_SOAK_CHECKPOINT") == "" {
		t.Fatal("soak requires exact sourceSHA and durable checkpoint path")
	}
	return time.Duration(n) * time.Second
}

func writeRolloverSoakCheckpoint(t *testing.T, c rolloverSoakCheckpoint) {
	t.Helper()
	path := os.Getenv("ROLLOVER_SOAK_CHECKPOINT")
	if path == "" {
		return
	}
	if c.Engines > 32 || c.References > 102 || c.OldActive < 2 {
		t.Fatal("soak resource/retention invariant", c.Engines, c.References, c.OldActive)
	}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	// The caller supplies a bind-mounted output directory, independent of test
	// tmpfs/compiler scratch. Never replace a production journal or source state.
	tmp, err := os.CreateTemp(filepath.Dir(path), "rollover-soak-*.tmp")
	if err != nil {
		t.Fatal(err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(data)
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(name, path); err != nil {
		t.Fatal(err)
	}
}
