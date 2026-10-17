package core

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"keep-mirrors/internal/config"
	"keep-mirrors/internal/env"
	"keep-mirrors/internal/fsutil"
	"keep-mirrors/internal/marker"
	"keep-mirrors/internal/paths"
)

// Backup is a point-in-time copy of everything a plan touched. It is written
// before the first modification of an apply, which is what makes undo a
// file-copy operation rather than an inverse transformation: we never have to
// trust that reversing a change reproduces the original.
type Backup struct {
	ID        string // timestamp-based, sortable
	Provider  string // provider that triggered the apply
	PresetID  string // preset that was applied
	Created   time.Time
	Files     []BackupFile
	EnvBefore map[string]string // persisted values before the apply
}

// BackupFile is one file captured by a backup.
type BackupFile struct {
	Path    string // original path
	Rel     string // path relative to the backup root
	Existed bool   // false when the file was created by the apply
}

// Root returns the backup directory for this backup.
func (b *Backup) Root() string { return filepath.Join(paths.BackupDir(), b.ID) }

// NewBackup captures the current state of the files and variables the plan
// touches. It is safe to call on a plan that has not been described yet.
func NewBackup(p *Plan) (*Backup, error) {
	id := fsutil.Timestamp(time.Now())
	b := &Backup{
		ID:        id,
		Provider:  p.Provider,
		PresetID:  p.PresetID,
		Created:   time.Now(),
		EnvBefore: map[string]string{},
	}
	root := b.Root()
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("create backup dir: %w", err)
	}

	// Files first, so a failure here happens before anything is modified.
	for _, f := range p.Files() {
		rel := backupRel(f)
		existed := fsutil.Exists(f)
		if existed {
			if err := fsutil.CopyFile(f, filepath.Join(root, rel)); err != nil {
				return nil, fmt.Errorf("backup %s: %w", f, err)
			}
		}
		b.Files = append(b.Files, BackupFile{Path: f, Rel: rel, Existed: existed})
	}

	for _, c := range p.Changes {
		if c.Kind != ChangeEnv && c.Kind != ChangeEnvUnset {
			continue
		}
		v, err := env.Read(c.Name)
		if err != nil {
			return nil, fmt.Errorf("backup env %s: %w", c.Name, err)
		}
		b.EnvBefore[c.Name] = v
	}

	// Environment values are serialised into the manifest rather than kept in
	// memory only, so a backup restored by a later run (km undo in a new
	// process) still knows what the variables were.
	meta := fmt.Sprintf("provider=%s\npreset=%s\ncreated=%s\n",
		p.Provider, p.PresetID, b.Created.Format(time.RFC3339))
	for _, f := range b.Files {
		meta += fmt.Sprintf("file=%s\t%s\texisted=%t\n", f.Rel, f.Path, f.Existed)
	}
	for _, name := range sortedEnvNames(b.EnvBefore) {
		meta += fmt.Sprintf("env=%s\t%s\n", name, b.EnvBefore[name])
	}
	if err := os.WriteFile(filepath.Join(root, "manifest.txt"), []byte(meta), 0o644); err != nil {
		return nil, fmt.Errorf("write manifest: %w", err)
	}
	return b, nil
}

// sortedEnvNames returns the environment names in stable order so the
// manifest is reproducible.
func sortedEnvNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Restore puts every captured file back and re-applies the captured
// environment values. Files that did not exist when the backup was taken are
// deleted, so an apply that created ~/.pip/pip.conf is undone by removing it.
func (b *Backup) Restore() error {
	root := b.Root()
	var errs []string

	for _, f := range b.Files {
		src := filepath.Join(root, f.Rel)
		if !f.Existed {
			if err := os.Remove(f.Path); err != nil && !os.IsNotExist(err) {
				errs = append(errs, fmt.Sprintf("remove %s: %v", f.Path, err))
			}
			continue
		}
		if err := fsutil.CopyFile(src, f.Path); err != nil {
			errs = append(errs, fmt.Sprintf("restore %s: %v", f.Path, err))
		}
	}

	for name, val := range b.EnvBefore {
		var err error
		if val == "" {
			err = env.Delete(name)
		} else {
			err = env.Write(name, val)
		}
		if err != nil {
			errs = append(errs, fmt.Sprintf("restore env %s: %v", name, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("restore incomplete:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}

// Prune keeps the newest keep backups and deletes the rest.
func Prune(keep int) (int, error) {
	list, err := ListBackups()
	if err != nil {
		return 0, err
	}
	if keep <= 0 || len(list) <= keep {
		return 0, nil
	}
	removed := 0
	for _, b := range list[keep:] {
		if err := os.RemoveAll(b.Root()); err == nil {
			removed++
		}
	}
	return removed, nil
}

// ListBackups returns all backups, newest first.
func ListBackups() ([]*Backup, error) {
	base := paths.BackupDir()
	entries, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []*Backup
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		meta, err := os.ReadFile(filepath.Join(base, e.Name(), "manifest.txt"))
		if err != nil {
			continue
		}
		b := &Backup{ID: e.Name(), EnvBefore: map[string]string{}}
		for _, line := range strings.Split(string(meta), "\n") {
			switch {
			case strings.HasPrefix(line, "provider="):
				b.Provider = strings.TrimPrefix(line, "provider=")
			case strings.HasPrefix(line, "preset="):
				b.PresetID = strings.TrimPrefix(line, "preset=")
			case strings.HasPrefix(line, "created="):
				b.Created, _ = time.Parse(time.RFC3339, strings.TrimPrefix(line, "created="))
			case strings.HasPrefix(line, "file="):
				parts := strings.Split(strings.TrimPrefix(line, "file="), "\t")
				if len(parts) == 3 {
					b.Files = append(b.Files, BackupFile{
						Rel:     parts[0],
						Path:    parts[1],
						Existed: parts[2] == "existed=true",
					})
				}
			case strings.HasPrefix(line, "env="):
				parts := strings.SplitN(strings.TrimPrefix(line, "env="), "\t", 2)
				if len(parts) == 2 {
					b.EnvBefore[parts[0]] = parts[1]
				}
			}
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

// backupRel turns an absolute path into a stable relative name inside the
// backup directory.
//
// Home-relative when possible; otherwise the volume name is kept but with its
// colon removed, because filepath.Join treats ":" as an ordinary character and
// would otherwise produce "abs\C:Users", which is not a valid path.
func backupRel(p string) string {
	h := paths.Home()
	if h != "" && strings.HasPrefix(p, h) {
		return filepath.Join("home", strings.TrimPrefix(p, h))
	}
	vol := filepath.VolumeName(p)
	vol = strings.ReplaceAll(vol, ":", "")
	rest := strings.TrimPrefix(p, filepath.VolumeName(p))
	rest = strings.TrimPrefix(rest, string(filepath.Separator))
	return filepath.Join("abs", vol, rest)
}

// unused keeps the imports that are only referenced by future steps honest.
var (
	_ = config.FormatINI
	_ = marker.Hash
	_ = paths.AppDir
)
