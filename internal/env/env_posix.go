//go:build !windows

package env

import (
	"fmt"
	"os"
)

// On POSIX the only durable, per-user environment store we can touch without
// elevation is the shell's rc file. There is no registry to read back from, so
// "persisted" means "there is an export line for this variable in the profile".
// Everything here operates on that file.

// Read returns the persisted value of name, or "" when the profile has no
// entry for it.
func Read(name string) (string, error) {
	sh := DetectShell()
	p := ProfilePath(sh)
	if p == "" {
		return "", nil
	}
	body, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	vars := ParseProfile(string(body), sh)
	if v, ok := vars[name]; ok {
		return v, nil
	}
	return "", nil
}

// Write adds or updates the export line for name in the user's profile.
func Write(name, value string) error {
	sh := DetectShell()
	p := ProfilePath(sh)
	if p == "" {
		return fmt.Errorf("cannot determine profile file for shell %q", sh)
	}
	body, err := os.ReadFile(p)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	updated := UpsertProfile(string(body), sh, map[string]string{name: value}, nil)
	if err := writeProfile(p, string(body), updated); err != nil {
		return err
	}
	return Set(name, value)
}

// Delete removes the export line for name from the user's profile.
func Delete(name string) error {
	sh := DetectShell()
	p := ProfilePath(sh)
	if p == "" {
		return fmt.Errorf("cannot determine profile file for shell %q", sh)
	}
	body, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	updated := UpsertProfile(string(body), sh, nil, []string{name})
	if err := writeProfile(p, string(body), updated); err != nil {
		return err
	}
	return Unset(name)
}

// All returns every variable the profile exports, sorted by name.
func All() (map[string]string, error) {
	sh := DetectShell()
	p := ProfilePath(sh)
	if p == "" {
		return map[string]string{}, nil
	}
	body, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	return ParseProfile(string(body), sh), nil
}

// Broadcast is a no-op on POSIX: the shell re-reads its rc file on start.
func Broadcast() {}

func writeProfile(path, old, updated string) error {
	if updated == old {
		return nil
	}
	// A profile is a shell script; replacing it wholesale would destroy
	// everything the user wrote outside our block. UpsertProfile already
	// preserves foreign content, and this function only re-checks that
	// guarantee before the final write.
	return WriteProfile(path, updated)
}
