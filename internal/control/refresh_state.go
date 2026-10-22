package control

import (
	"proxyplatform/internal/catalog"
	"strings"
	"time"
)

// A pending snapshot may have been classified by an older release. Refetch it
// once per normal refresh with no conditional validators, then run every current
// trust, filter, partial-update and revision fence. Never replay its approval.
func sourceForRefresh(source catalog.Source, action string) catalog.Source {
	if action != "test" && source.PendingSnapshot == "" {
		return source
	}
	source.ETag, source.LastModified = "", ""
	headers := make(map[string]string, len(source.Headers))
	for k, v := range source.Headers {
		if !strings.EqualFold(k, "If-None-Match") && !strings.EqualFold(k, "If-Modified-Since") {
			headers[k] = v
		}
	}
	source.Headers = headers
	return source
}

func sourceNotModified(source catalog.Source, quota map[string]int64, now time.Time) catalog.Source {
	source.LastFetched = now
	if len(quota) > 0 {
		source.Quota = quota
	}
	// A broken server can return 304 even to an unconditional request. That does
	// not confirm any held record or grant more lifetime to an unaccepted body.
	if source.PendingSnapshot == "" {
		source.LastSuccess = now
		source.LastError = ""
	}
	source.NextRefresh = now.Add(time.Duration(max(source.IntervalSeconds, 60)) * time.Second)
	return source
}
