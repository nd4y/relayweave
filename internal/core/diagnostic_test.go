package core

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestCoreDiagnosticExplainsFailureWithoutLeakingInput(t *testing.T) {
	previous := slog.Default()
	defer slog.SetDefault(previous)
	for _, test := range []struct{ detail, reason string }{
		{"failed to listen: address already in use", "listener_address_in_use"},
		{"failed to open config: no such file or directory", "configuration_file_missing"},
		{"failed to load: permission denied", "permission_denied"},
		{"failed outbound authentication", "redacted"},
	} {
		t.Run(test.reason, func(t *testing.T) {
			var output bytes.Buffer
			slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
			input := test.detail + " https://device:dummy-sensitive-token@example.invalid/private-subscription"
			writer := &coreLog{core: "fixture"}
			if n, err := writer.Write([]byte(input)); err != nil || n != len(input) {
				t.Fatalf("write=%d error=%v", n, err)
			}
			if strings.Contains(output.String(), "dummy-sensitive-token") || strings.Contains(output.String(), "private-subscription") {
				t.Fatal("untrusted diagnostic content escaped redaction")
			}
			var event map[string]any
			if err := json.Unmarshal(output.Bytes(), &event); err != nil {
				t.Fatal(err)
			}
			if event["reason"] != test.reason || event["detail"] != "redacted" {
				t.Fatalf("wrong safe diagnostic: %v", event)
			}
		})
	}
}
