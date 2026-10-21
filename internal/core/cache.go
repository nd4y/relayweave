package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

var cacheSelector = regexp.MustCompile(`^[0-9]{1,2}\.[0-9]{1,2}\.[0-9]{1,2}(-[a-z][a-z0-9-]{0,23})?$`)

type CacheEntry struct {
	Selector  string   `json:"selector"`
	Bytes     int64    `json:"bytes"`
	Protected bool     `json:"protected"`
	Reasons   []string `json:"reasons"`
}
type CacheStatus struct {
	Items        []CacheEntry `json:"items"`
	Bytes        int64        `json:"bytes"`
	FreeBytes    uint64       `json:"free_bytes"`
	MaxEntries   int          `json:"max_entries"`
	ReserveBytes uint64       `json:"reserve_bytes"`
}

func Cache(root string, protected map[string][]string) (CacheStatus, error) {
	out := CacheStatus{Items: []CacheEntry{}, MaxEntries: 8, ReserveBytes: 256 << 20}
	entries, err := os.ReadDir(root)
	if err != nil {
		return out, err
	}
	for _, entry := range entries {
		selector := strings.TrimPrefix(entry.Name(), "v")
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "v") || !cacheSelector.MatchString(selector) {
			continue
		}
		size := int64(0)
		if err = filepath.WalkDir(filepath.Join(root, entry.Name()), func(_ string, d os.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if d.Type().IsRegular() {
				v, e := d.Info()
				if e != nil {
					return e
				}
				size += v.Size()
			}
			return nil
		}); err != nil {
			return out, err
		}
		reasons := protected[selector]
		if reasons == nil {
			reasons = []string{}
		}
		out.Items = append(out.Items, CacheEntry{Selector: selector, Bytes: size, Protected: len(reasons) > 0, Reasons: reasons})
		out.Bytes += size
	}
	sort.Slice(out.Items, func(i, j int) bool { return out.Items[i].Selector < out.Items[j].Selector })
	var disk syscall.Statfs_t
	if err = syscall.Statfs(root, &disk); err != nil {
		return out, err
	}
	out.FreeBytes = disk.Bavail * uint64(disk.Bsize)
	return out, nil
}

// Callers must serialize against configuration preparation and supply every
// active, draining and recoverable selector. The image's builtin is immutable.
func RemoveCached(ctx context.Context, root, selector string, protected map[string][]string) error {
	if !cacheSelector.MatchString(selector) {
		return fmt.Errorf("invalid_core_version")
	}
	if selector == "26.10.17" || len(protected[selector]) > 0 {
		return fmt.Errorf("artifact_cache_in_use")
	}
	lf, err := os.OpenFile(filepath.Join(root, ".install.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lf.Close()
	for {
		err = syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)
	dest := filepath.Join(root, "v"+selector)
	info, err := os.Lstat(dest)
	if os.IsNotExist(err) {
		return fmt.Errorf("artifact_cache_not_found")
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("artifact_cache_path_invalid")
	}
	return os.RemoveAll(dest)
}
