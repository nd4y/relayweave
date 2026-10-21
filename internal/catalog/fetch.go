package catalog

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type FetchResult struct {
	Body         []byte
	ETag         string
	LastModified string
	NotModified  bool
	Quota        map[string]int64
}

func Fetch(ctx context.Context, s Source) (FetchResult, error) {
	var result FetchResult
	u, e := url.Parse(s.URL)
	if e != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return result, fmt.Errorf("source_url_invalid")
	}
	if e = checkHost(ctx, u.Hostname(), s.AllowPrivate); e != nil {
		return result, e
	}
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, ResponseHeaderTimeout: 15 * time.Second, MaxResponseHeaderBytes: 64 << 10,
		DialContext: func(ctx context.Context, network, target string) (net.Conn, error) {
			host, port, e := net.SplitHostPort(target)
			if e != nil {
				return nil, e
			}
			ips, e := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
			if e != nil {
				return nil, fmt.Errorf("source_dns_failed")
			}
			var last error
			for _, ip := range ips {
				if !permitted(ip, s.AllowPrivate) {
					return nil, fmt.Errorf("source_address_blocked")
				}
				c, e := (&net.Dialer{Timeout: 8 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				if e == nil {
					return c, nil
				}
				last = e
			}
			if last != nil {
				return nil, fmt.Errorf("source_connect_failed")
			}
			return nil, fmt.Errorf("source_dns_empty")
		}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: 25 * time.Second, Transport: transport, CheckRedirect: func(req *http.Request, previous []*http.Request) error {
		if len(previous) >= 4 {
			return fmt.Errorf("redirect_limit")
		}
		if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
			return fmt.Errorf("redirect_scheme")
		}
		if req.URL.User != nil || (u.Scheme == "https" && req.URL.Scheme != "https") {
			return fmt.Errorf("redirect_security_policy")
		}
		req.Header.Del("Referer")
		if !strings.EqualFold(req.URL.Host, u.Host) {
			for k := range s.Headers {
				req.Header.Del(k)
			}
			req.Header.Del("Authorization")
			req.Header.Del("Cookie")
		}
		return checkHost(req.Context(), req.URL.Hostname(), s.AllowPrivate)
	}}
	req, e := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if e != nil {
		return result, fmt.Errorf("source_request_invalid")
	}
	req.Header.Set("User-Agent", "RelayWeave/0.1")
	req.Header.Set("Accept", "application/json, text/plain, application/yaml;q=0.9, */*;q=0.1")
	for k, v := range s.Headers {
		if strings.ContainsAny(k+v, "\r\n") || len(k) > 128 || len(v) > 4096 || strings.EqualFold(k, "Host") || strings.EqualFold(k, "Connection") {
			return result, fmt.Errorf("source_header_invalid")
		}
		req.Header.Set(k, v)
	}
	if s.ETag != "" {
		req.Header.Set("If-None-Match", s.ETag)
	}
	if s.LastModified != "" {
		req.Header.Set("If-Modified-Since", s.LastModified)
	}
	resp, e := client.Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return result, fmt.Errorf("source_cancelled")
		}
		return result, fmt.Errorf("source_fetch_failed")
	}
	defer resp.Body.Close()
	result.Quota = parseQuota(resp.Header.Get("Subscription-Userinfo"))
	if resp.StatusCode == 304 {
		result.NotModified = true
		return result, nil
	}
	if resp.StatusCode != 200 {
		return result, fmt.Errorf("source_http_%d", resp.StatusCode)
	}
	body, e := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if e != nil {
		return result, fmt.Errorf("source_read_failed")
	}
	if len(body) > 4<<20 {
		return result, fmt.Errorf("source_size_limit")
	}
	result.Body = body
	result.ETag = resp.Header.Get("ETag")
	result.LastModified = resp.Header.Get("Last-Modified")
	return result, nil
}
func parseQuota(raw string) map[string]int64 {
	result := map[string]int64{}
	for _, item := range strings.Split(raw, ";") {
		key, v, ok := strings.Cut(strings.TrimSpace(item), "=")
		if ok && contains([]string{"upload", "download", "total", "expire"}, key) {
			if n, e := strconv.ParseInt(v, 10, 64); e == nil && n >= 0 {
				result[key] = n
			}
		}
	}
	return result
}
func permitted(ip netip.Addr, allowPrivate bool) bool {
	ip = ip.Unmap()
	if ip.String() == "fd00:ec2::254" || ip.String() == "100.100.100.200" {
		return false
	}
	if !ip.IsValid() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	if !ip.IsGlobalUnicast() {
		return false
	}
	if netip.MustParsePrefix("100.64.0.0/10").Contains(ip) && !allowPrivate {
		return false
	}
	if ip.IsPrivate() && !allowPrivate {
		return false
	}
	return true
}
func checkHost(ctx context.Context, host string, allowPrivate bool) error {
	ips, e := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if e != nil {
		return fmt.Errorf("source_dns_failed")
	}
	if len(ips) == 0 {
		return fmt.Errorf("source_dns_empty")
	}
	for _, ip := range ips {
		if !permitted(ip, allowPrivate) {
			return fmt.Errorf("source_address_blocked")
		}
	}
	return nil
}
