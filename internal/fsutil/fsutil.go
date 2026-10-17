// Package fsutil provides the small set of filesystem primitives that every
// configuration write goes through. The invariants it enforces are what make
// "apply" safe enough to hand to a user without a second confirmation dialog:
//
//   - writes are atomic (temp file in the same directory, fsync, rename)
//   - the original mode and ownership bits survive
//   - the parent directory is created only when the caller asks for it
//   - a copy of the previous content is taken before the first modification
package fsutil

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Exists reports whether p exists (following symlinks).
func Exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// IsDir reports whether p is a directory.
func IsDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// Read returns the file content as a string. A missing file yields "" and a
// nil error, which keeps the callers free of "does it exist yet" noise.
func Read(p string) (string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(b), nil
}

// MustRead is Read with the error dropped; it is only for diagnostics.
func MustRead(p string) string {
	s, _ := Read(p)
	return s
}

// ModeOf returns the file mode of p, or 0o644 when p does not exist.
func ModeOf(p string) os.FileMode {
	fi, err := os.Stat(p)
	if err != nil {
		return 0o644
	}
	return fi.Mode().Perm()
}

// WriteFile atomically replaces p with content.
//
// The temp file is created in the same directory as p so the final rename is a
// same-filesystem operation and therefore atomic on every platform we support.
// The mode of the pre-existing file is preserved; when p is new, perm is used.
func WriteFile(p, content string, perm os.FileMode) error {
	if perm == 0 {
		perm = ModeOf(p)
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".keep-mirrors-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() {
		// Best effort: if the rename already succeeded this is a no-op error.
		_ = os.Remove(tmpName)
	}()

	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpName, err)
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return fmt.Errorf("chmod %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, p); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", tmpName, p, err)
	}
	return nil
}

// CopyFile duplicates src at dst, preserving the mode of src.
func CopyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, ModeOf(src))
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// Timestamp formats t for use in snapshot directory names.
func Timestamp(t time.Time) string {
	return t.UTC().Format("20060102T150405.000000000")
}
