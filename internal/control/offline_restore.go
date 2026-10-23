package control

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"proxyplatform/internal/catalog"
	"proxyplatform/internal/store"
)

// RestoreOffline restores configuration to a stopped control plane. New mTLS
// identities are retained, so recovery to a new deployment never clones worker
// identities. The backup remains password encrypted until this explicit action.
func RestoreOffline(ctx context.Context, file, password, dataDir string, replace bool) (map[string]int, error) {
	input, err := os.Open(file)
	if err != nil {
		return nil, fmt.Errorf("backup_file_unavailable")
	}
	defer input.Close()
	encrypted, err := store.ReadBounded(input, 64<<20)
	if err != nil {
		return nil, fmt.Errorf("backup_size_limit")
	}
	compressed, err := decryptBackup(encrypted, password)
	if err != nil {
		return nil, fmt.Errorf("backup_password_or_integrity")
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, fmt.Errorf("backup_compression_invalid")
	}
	defer reader.Close()
	plain, err := store.ReadBounded(reader, 64<<20)
	if err != nil {
		return nil, fmt.Errorf("backup_size_limit")
	}
	var bundle backupBundle
	if json.Unmarshal(plain, &bundle) != nil || bundle.Schema != 1 || validateBackup(bundle.Export) != nil {
		return nil, fmt.Errorf("backup_configuration_invalid")
	}
	s, err := store.New(ctx, os.Getenv("DATABASE_URL"), filepath.Join(dataDir, "master.key"))
	if err != nil {
		return nil, err
	}
	defer s.DB.Close()
	release, err := s.AdvisoryLock(ctx, 723501991)
	if err != nil {
		return nil, err
	}
	defer release()
	var existing int
	if err = s.DB.QueryRow(ctx, `SELECT count(*) FROM objects WHERE kind IN ('sources','profiles','aggregates','pools','policies','listeners')`).Scan(&existing); err != nil {
		return nil, err
	}
	if existing > 0 && !replace {
		return nil, fmt.Errorf("restore_target_not_empty_use_replace")
	}
	counts := map[string]int{}
	for i, o := range bundle.Export.Objects {
		counts[o.Kind]++
		if o.Kind == "sources" {
			v, _ := catalog.Decode[catalog.Source](o.Data)
			v.PendingSnapshot = ""
			o.Data, _ = json.Marshal(v)
			bundle.Export.Objects[i] = o
		}
	}
	if err = s.Import(ctx, bundle.Export); err != nil {
		return nil, fmt.Errorf("backup_import_failed")
	}
	return counts, nil
}
