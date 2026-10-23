package core

import (
	"archive/zip"
	"crypto/sha256"
	endian "encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

// InspectArchive verifies both layers without executing the downloaded binary.
func InspectArchive(path, expectedArchive string, arches ...string) (string, error) {
	digest, e := FileHash(path)
	if e != nil || digest != expectedArchive {
		return "", fmt.Errorf("archive_digest_mismatch")
	}
	archive, e := zip.OpenReader(path)
	if e != nil {
		return "", e
	}
	defer archive.Close()
	var binary *zip.File
	for _, f := range archive.File {
		if f.Name == "xray" {
			if binary != nil {
				return "", fmt.Errorf("duplicate_binary")
			}
			binary = f
		}
	}
	if binary == nil || binary.UncompressedSize64 > 128<<20 || binary.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("invalid_archive_binary")
	}
	r, e := binary.Open()
	if e != nil {
		return "", e
	}
	defer r.Close()
	header := make([]byte, 64)
	if _, e = io.ReadFull(r, header); e != nil || string(header[:4]) != "\x7fELF" || header[4] != 2 || header[5] != 1 {
		return "", fmt.Errorf("linux_64bit_elf_required")
	}
	machine := endian.LittleEndian.Uint16(header[18:20])
	if len(arches) > 0 {
		expected := map[string]uint16{"amd64": 62, "arm64": 183}[arches[0]]
		if expected == 0 || machine != expected {
			return "", fmt.Errorf("binary_architecture_mismatch")
		}
	}
	hash := sha256.New()
	hash.Write(header)
	n, e := io.Copy(hash, io.LimitReader(r, (128<<20)+1-64))
	if e != nil || n+64 > 128<<20 {
		return "", fmt.Errorf("binary_size_or_integrity")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
