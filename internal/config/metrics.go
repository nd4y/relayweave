package config

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
)

var workerName = regexp.MustCompile(`^worker-[a-z0-9][a-z0-9-]{0,40}$`)
var metricsHost = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9_.-]{0,251}[A-Za-z0-9])?$`)

// MetricsTarget accepts a dial address, never a URL, credentials or a path.
// A remote worker must advertise an address reachable from Prometheus.
func MetricsTarget(worker, configured string) (string, error) {
	if !workerName.MatchString(worker) {
		return "", fmt.Errorf("invalid worker metrics identity")
	}
	if configured == "" {
		return net.JoinHostPort(worker, "9090"), nil
	}
	host, port, err := net.SplitHostPort(configured)
	n, numberErr := strconv.Atoi(port)
	if err != nil || numberErr != nil || n < 1 || n > 65535 || len(host) > 253 {
		return "", fmt.Errorf("invalid worker metrics target")
	}
	ip := net.ParseIP(host)
	if (ip == nil && !metricsHost.MatchString(host)) || (ip != nil && ip.IsUnspecified()) {
		return "", fmt.Errorf("invalid worker metrics host")
	}
	return net.JoinHostPort(host, strconv.Itoa(n)), nil
}
