package compute

import (
	"encoding/json"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
	"proxyplatform/internal/identity"
)

// ReplaceFileW retains the existing service identity file's ACL. In particular,
// a renewal must not replace its ACL with Go's Unix-mode approximation.
func saveRenewedBundle(path string, bundle identity.Bundle) error {
	raw, e := json.Marshal(bundle)
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".compute-renew-")
	if e != nil {
		return e
	}
	temporary := f.Name()
	defer os.Remove(temporary)
	if _, e = f.Write(raw); e == nil {
		e = f.Sync()
	}
	closed := f.Close()
	if e != nil {
		return e
	}
	if closed != nil {
		return closed
	}
	original, e := windows.UTF16PtrFromString(path)
	if e != nil {
		return e
	}
	replacement, e := windows.UTF16PtrFromString(temporary)
	if e != nil {
		return e
	}
	ok, _, e := kernel.NewProc("ReplaceFileW").Call(uintptr(unsafe.Pointer(original)), uintptr(unsafe.Pointer(replacement)), 0, 0, 0, 0)
	if ok == 0 {
		return e
	}
	return nil
}
