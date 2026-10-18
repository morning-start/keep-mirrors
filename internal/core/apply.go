package core

import (
	"fmt"

	"keep-mirrors/internal/env"
	"keep-mirrors/internal/fsutil"
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
		case ChangeConfig, ChangeConfigRemove:
			// c.After was computed by Describe from the current on-disk
			// state, so writing it is exactly writing the diff the user
			// approved. For a removal, After is the content with the block
			// stripped.
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

// Undo restores a backup.
func Undo(b *Backup) error { return b.Restore() }
