package config

import "os"

// MetricsAddress is an explicit deployment bind, not a proxy endpoint.
func MetricsAddress() string {
	if value := os.Getenv("WORKER_METRICS_LISTEN"); value != "" {
		return value
	}
	return "0.0.0.0:9090"
}
