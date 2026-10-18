package syncs

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"keep-mirrors/internal/fsutil"
	"keep-mirrors/internal/paths"
)

// Result reports what a sync did.
type Result struct {
	// SHA is the commit the plugins now come from.
	SHA string
	// Updated reports whether anything was written.
	Updated bool
	// Before and After are the commit SHAs around the sync; Before is empty on
	// a first sync.
	Before string
	// FromSeed reports that Before came from the snapshot compiled into the
	// binary rather than from a previous sync.
	FromSeed bool
	// Files lists the plugin files written.
	Files []string
	// Version is the plugin set version, when the set declares one.
	Version string
}

// Options adjusts a sync run.
type Options struct {
	Config
	// DryRun resolves and validates without writing anything.
	DryRun bool
	// Force re-downloads even when the recorded SHA matches.
	Force bool
}

// knownPlugins is the plugin id set the binary ships. A sync that would remove
// one of these is refused: dropping a source silently is far more likely to be
// an upstream mistake than an intentional removal.
//
// This is deliberately a "must still be present" list rather than an exact set:
// the branch may add plugins the running binary has never seen, and those are
// fine to install.
var knownPlugins = []string{"pip", "npm", "cargo", "go", "flutter"}

// Run performs the sync.
//
// The write is all-or-nothing with respect to validation: every downloaded file
// is parsed against the contract first, and nothing touches the official
// directory until the whole set passes. A half-valid batch would leave the user
// with a plugin directory that partly loads, which is harder to diagnose than a
// refused sync.
func Run(ctx context.Context, opts Options) (Result, error) {
	cfg := opts.Config
	if cfg.Repo == "" {
		cfg.Repo = DefaultRepo
	}
	if cfg.Branch == "" {
		cfg.Branch = DefaultBranch
	}
	if cfg.Dir == "" {
		cfg.Dir = paths.OfficialPluginDir()
	}
	return run(ctx, newGitRemote(), cfg, opts)
}

// run is the testable body: it takes the remote as a parameter.
func run(ctx context.Context, r remote, cfg Config, opts Options) (Result, error) {
	var res Result

	prev, err := LoadState()
	if err != nil {
		return res, err
	}
	res.Before = prev.SHA
	res.FromSeed = prev.FromSeed

	sha, err := r.BranchSHA(ctx, cfg.Repo, cfg.Branch)
	if err != nil {
		return res, err
	}
	if !ValidSHA(sha) {
		return res, fmt.Errorf("resolved %s/%s to %q, which is not a commit SHA", cfg.Repo, cfg.Branch, sha)
	}
	res.SHA = sha

	if sha == prev.SHA && !opts.Force {
		res.Updated = false
		res.Version = prev.Version
		return res, nil
	}
	if opts.DryRun {
		return res, nil
	}

	files, ver, err := fetchSet(ctx, r, cfg, sha)
	if err != nil {
		return res, err
	}
	if err := writeSet(cfg.Dir, files); err != nil {
		return res, err
	}

	res.Updated = true
	res.Version = ver
	for name := range files {
		res.Files = append(res.Files, name)
	}
	sort.Strings(res.Files)

	if err := SaveState(State{Repo: cfg.Repo, Branch: cfg.Branch, SHA: sha, Version: ver}); err != nil {
		return res, err
	}
	return res, nil
}

// fetchSet downloads every plugin in the curated set and validates the whole
// set in memory before anything is written.
func fetchSet(ctx context.Context, r remote, cfg Config, sha string) (map[string][]byte, string, error) {
	names := curatedNames()
	if len(names) == 0 {
		return nil, "", fmt.Errorf("no plugins to sync")
	}

	files := make(map[string][]byte, len(names))
	versions := map[string]bool{}
	for _, name := range names {
		path := PluginDirInRepo + "/" + name
		body, err := r.File(ctx, cfg.Repo, sha, path)
		if err != nil {
			// A curated plugin missing at the pinned commit is the drop-a-source
			// case, not a download failure. Reporting it here rather than as a
			// generic fetch error is what makes the message actionable: the
			// earlier code aborted on the 404 and never reached the completeness
			// check, so the user saw "HTTP 404" for what is really an upstream
			// mistake.
			if errors.Is(err, ErrNotFound) {
				return nil, "", fmt.Errorf(
					"the plugin set at %s is missing %s; refusing to drop sources that this binary ships",
					shortSHA(sha), idOf(name))
			}
			return nil, "", err
		}
		ver, err := validatePlugin(name, body)
		if err != nil {
			return nil, "", fmt.Errorf("%s: %w", name, err)
		}
		files[name] = body
		if ver != "" {
			versions[ver] = true
		}
	}

	if missing := missingKnown(files); len(missing) > 0 {
		return nil, "", fmt.Errorf(
			"the plugin set at %s is missing %s; refusing to drop sources that this binary ships",
			shortSHA(sha), strings.Join(missing, ", "))
	}
	return files, setVersion(versions), nil
}

// idOf turns "pip.lua" into "pip", for messages.
func idOf(name string) string { return strings.TrimSuffix(name, ".lua") }

// curatedNames lists the .lua files in the curated set.
//
// The list comes from the embedded snapshot rather than from the network:
// the files this binary must still have are exactly the ones it knows about,
// and asking the network for a directory listing would mean the REST API this
// design deliberately avoids. Unknown extra plugins on the branch are simply
// not fetched — the branch can add sources, but a running binary only grows its
// set through a release.
func curatedNames() []string {
	names := make([]string, 0, len(knownPlugins))
	for _, id := range knownPlugins {
		names = append(names, id+".lua")
	}
	return names
}

// writeSet writes the validated plugins into dir, removing stale .lua files
// that are no longer part of the set.
//
// Each file goes through fsutil.WriteFile, so a crash mid-write leaves either
// the old plugin or the new one, never a truncated file that fails to compile.
func writeSet(dir string, files map[string][]byte) error {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, name := range names {
		dst := filepath.Join(dir, name)
		if err := fsutil.WriteFile(dst, string(files[name]), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", dst, err)
		}
	}
	return nil
}

// missingKnown returns the ids from knownPlugins that are absent from files.
func missingKnown(files map[string][]byte) []string {
	var out []string
	for _, id := range knownPlugins {
		if _, ok := files[id+".lua"]; !ok {
			out = append(out, id)
		}
	}
	return out
}

// shortSHA abbreviates a SHA for messages.
func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}
