package control

import (
	"bytes"
	"compress/gzip"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/scrypt"
	"proxyplatform/internal/catalog"
	"proxyplatform/internal/store"
)

type backupBundle struct {
	Schema   int               `json:"schema"`
	Export   store.Export      `json:"export"`
	Identity map[string][]byte `json:"identity"`
}

func encryptBackup(plain []byte, password string) ([]byte, error) {
	salt := make([]byte, 16)
	if _, e := rand.Read(salt); e != nil {
		return nil, e
	}
	key, e := scrypt.Key([]byte(password), salt, 32768, 8, 1, 32)
	if e != nil {
		return nil, e
	}
	block, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	a, e := cipher.NewGCM(block)
	if e != nil {
		return nil, e
	}
	nonce := make([]byte, a.NonceSize())
	if _, e = rand.Read(nonce); e != nil {
		return nil, e
	}
	header := append([]byte("RWBACKUP1"), salt...)
	header = append(header, nonce...)
	return a.Seal(header, nonce, plain, header[:25]), nil
}
func decryptBackup(data []byte, password string) ([]byte, error) {
	if len(data) < 37 || string(data[:9]) != "RWBACKUP1" {
		return nil, fmt.Errorf("invalid_backup")
	}
	key, e := scrypt.Key([]byte(password), data[9:25], 32768, 8, 1, 32)
	if e != nil {
		return nil, e
	}
	block, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	a, e := cipher.NewGCM(block)
	if e != nil {
		return nil, e
	}
	plain, e := a.Open(nil, data[25:37], data[37:], data[:25])
	if e != nil {
		return nil, fmt.Errorf("backup_password_or_integrity")
	}
	return plain, nil
}
func (s *Server) backup(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		fail(w, 405, "method_not_allowed")
		return
	}
	if strings.HasSuffix(r.URL.Path, "/restore") || strings.HasSuffix(r.URL.Path, "/preview") {
		r.Body = http.MaxBytesReader(w, r.Body, 64<<20)
		if e := r.ParseMultipartForm(8 << 20); e != nil {
			fail(w, 400, "backup_upload_invalid")
			return
		}
		defer r.MultipartForm.RemoveAll()
		file, _, e := r.FormFile("file")
		if e != nil {
			fail(w, 400, "backup_file_required")
			return
		}
		defer file.Close()
		data, e := store.ReadBounded(file, 64<<20)
		if e != nil {
			fail(w, 413, "backup_size_limit")
			return
		}
		password := r.FormValue("password")
		if password == "" {
			password = os.Getenv("ADMIN_PASSWORD")
		}
		compressed, e := decryptBackup(data, password)
		if e != nil {
			fail(w, 400, "backup_password_or_integrity")
			return
		}
		reader, e := gzip.NewReader(bytes.NewReader(compressed))
		if e != nil {
			fail(w, 400, "backup_compression_invalid")
			return
		}
		defer reader.Close()
		plain, e := store.ReadBounded(reader, 64<<20)
		if e != nil {
			fail(w, 413, "backup_size_limit")
			return
		}
		var b backupBundle
		if json.Unmarshal(plain, &b) != nil || b.Schema != 1 {
			fail(w, 400, "backup_schema_invalid")
			return
		}
		counts := map[string]int{}
		for _, o := range b.Export.Objects {
			counts[o.Kind]++
		}
		if e = validateBackup(b.Export); e != nil {
			fail(w, 400, "backup_configuration_invalid")
			return
		}
		if strings.HasSuffix(r.URL.Path, "/preview") {
			respond(w, 200, map[string]any{"valid": true, "counts": counts, "created_at": b.Export.Created, "apply_required": true, "deployment_identity_preserved": true})
			return
		}
		for i, o := range b.Export.Objects {
			if o.Kind == "sources" {
				v, _ := catalog.Decode[catalog.Source](o.Data)
				v.PendingSnapshot = ""
				o.Data, _ = json.Marshal(v)
				b.Export.Objects[i] = o
			}
		}
		release, err := s.Store.AdvisoryLock(r.Context(), 723501991)
		if err != nil {
			fail(w, 503, "backup_import_failed")
			return
		}
		defer release()
		if e = s.Store.Import(r.Context(), b.Export); e != nil {
			fail(w, 400, "backup_import_failed")
			return
		}
		s.Store.Event(r.Context(), "backup", "restore", "restored", counts)
		s.notify()
		respond(w, 200, map[string]any{"restored": true, "counts": counts, "apply_required": true, "deployment_identity_preserved": true})
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if r.ContentLength != 0 && !decode(w, r, &body) {
		return
	}
	password := body.Password
	if password == "" {
		password = os.Getenv("ADMIN_PASSWORD")
	}
	if len(password) < 12 {
		fail(w, 400, "backup_password_too_short")
		return
	}
	export, e := s.Store.Export(r.Context())
	if e != nil {
		fail(w, 503, "backup_export_failed")
		return
	}
	bundle := backupBundle{Schema: 1, Export: export, Identity: map[string][]byte{}}
	for _, name := range []string{"ca.pem", "ca.key", "server.json"} {
		b, e := os.ReadFile(filepath.Join(s.IdentityDir, name))
		if e != nil {
			fail(w, 503, "identity_read_failed")
			return
		}
		bundle.Identity[name] = b
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if e = json.NewEncoder(writer).Encode(bundle); e != nil {
		writer.Close()
		fail(w, 500, "backup_encode_failed")
		return
	}
	if e = writer.Close(); e != nil {
		fail(w, 500, "backup_encode_failed")
		return
	}
	encrypted, e := encryptBackup(compressed.Bytes(), password)
	if e != nil {
		fail(w, 500, "backup_encrypt_failed")
		return
	}
	s.Store.Event(r.Context(), "backup", "download", "created", map[string]any{"objects": len(export.Objects)})
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="relayweave-%s.rwb"`, time.Now().UTC().Format("20060102T150405Z")))
	w.WriteHeader(200)
	io.Copy(w, bytes.NewReader(encrypted))
}
