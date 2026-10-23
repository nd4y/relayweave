package control

import (
	"reflect"
	"time"

	"proxyplatform/internal/catalog"
	"proxyplatform/internal/store"
)

// Fetch settings affect the next subscription response, not the current paths.
// Avoid making a successful refresh wait behind an obsolete rollout caused by
// editing its URL or headers. A successful refresh always queues reconciliation.
func sourceRuntimeChanged(previous, updated store.Object) bool {
	if previous.Name != updated.Name {
		return true
	}
	before, err := catalog.Decode[catalog.Source](previous.Data)
	if err != nil {
		return true
	}
	after, err := catalog.Decode[catalog.Source](updated.Data)
	if err != nil {
		return true
	}
	for _, source := range []*catalog.Source{&before, &after} {
		source.URL = ""
		source.Headers = nil
		source.IntervalSeconds = 0
		source.AllowPrivate = false
		source.ETag = ""
		source.LastModified = ""
		source.NextRefresh = time.Time{}
		source.LastError = ""
		source.PendingSnapshot = ""
	}
	// Everything else, including future fields, remains significant by default.
	return !reflect.DeepEqual(before, after)
}
