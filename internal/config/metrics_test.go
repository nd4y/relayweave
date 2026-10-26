package config

import "testing"

func TestMetricsTarget(t *testing.T) {
	for input, want := range map[string]string{"": "worker-2:9090", "node.home:19090": "node.home:19090", "100.100.1.2:9090": "100.100.1.2:9090", "[fd00::2]:9090": "[fd00::2]:9090"} {
		got, err := MetricsTarget("worker-2", input)
		if err != nil || got != want {
			t.Fatalf("%q: %q %v", input, got, err)
		}
	}
	for _, input := range []string{"http://node:9090/metrics", "user@node:9090", "node:9090/path", "node:0", "node:65536", "node:-1", ":9090", "0.0.0.0:9090", "[::]:9090", "[fe80::1%eth0]:9090", "node\n:9090"} {
		if _, err := MetricsTarget("worker-2", input); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
	if _, err := MetricsTarget("worker-../../outside", "node:9090"); err == nil {
		t.Fatal("accepted invalid worker identity")
	}
}
