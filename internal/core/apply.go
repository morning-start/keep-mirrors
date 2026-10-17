package core

import (
	"fmt"
	"os"

	"keep-mirrors/internal/config"
	"keep-mirrors/internal/env"
	"keep-mirrors/internal/fsutil"
	"keep-mirrors/internal/marker"
)

// Apply executes a described plan, taking a backup first.
//
// The order matters: backup, then files, then environment. Files are written
// with fsutil.WriteFile, which is atomic, so a crash mid-apply leaves either
// the old file or the new one — never a truncated config.
//
// Apply re-describes the plan first. Between the user reading a diff and
// confirming it, the file may have changed (another editor, another tool), and
// applying a stale diff would silently discard that edit.
func Apply(p *Plan) (*Backup, error) {
	if err := p.Describe(); err != nil {
		return nil, err
	}
	if p.Empty() {
		return nil, nil
	}

	bak, err := NewBackup(p)
	if err != nil {
		return nil, fmt.Errorf("backup: %w", err)
	}

	for _, c := range p.Changes {
		switch c.Kind {
		case ChangeConfig:
			// c.After was computed by Describe from the current on-disk
			// state, so writing it is exactly writing the diff the user
			// approved.
			if err := fsutil.WriteFile(c.File, c.After, 0); err != nil {
				return bak, fmt.Errorf("write %s: %w", c.File, err)
			}

		case ChangeEnv:
			if err := env.Write(c.Name, c.Value); err != nil {
				return bak, fmt.Errorf("set env %s: %w", c.Name, err)
			}

		case ChangeEnvUnset:
			if err := env.Delete(c.Name); err != nil {
				return bak, fmt.Errorf("unset env %s: %w", c.Name, err)
			}
		}
	}
	return bak, nil
}

// ApplyFile writes a single managed block. It is the primitive the providers
// use for "make sure this file contains this setting", independent of any
// plan, and is what the engine uses for one-off repairs.
func ApplyFile(file string, f marker.Format, cf config.Format, block string, entries ...config.Entry) error {
	cur, err := fsutil.Read(file)
	if err != nil {
		return err
	}
	body := config.Render(cf, entries)
	out := marker.Replace(cur, marker.Block{Name: block, Format: f, Body: body})
	if out == cur {
		return nil
	}
	return fsutil.WriteFile(file, out, 0)
}

// ClearFile removes a managed block from a file, if present.
func ClearFile(file string, f marker.Format, block string) (bool, error) {
	cur, err := fsutil.Read(file)
	if err != nil {
		return false, err
	}
	out := marker.Remove(cur, block, f)
	if out == cur {
		return false, nil
	}
	if out == "" {
		// An empty file is worse than no file for tools that stat it.
		if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
			return false, err
		}
		return true, nil
	}
	return true, fsutil.WriteFile(file, out, 0)
}

// Undo restores a backup.
func Undo(b *Backup) error { return b.Restore() }
