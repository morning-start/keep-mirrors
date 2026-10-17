package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// fileStamp is the cheap identity of a plugin file. It is what the watcher
// compares to decide whether a reload is needed.
//
// Modification time alone is not enough: two edits inside one filesystem
// timestamp tick are indistinguishable, and a file rewritten to the same size
// within that tick would look unchanged. Size breaks most of those ties for a
// few bytes of work. Hashing every file on every poll would be exact but would
// read the whole plugin set once a second forever, which is a bad trade for a
// tool that is mostly idle.
type fileStamp struct {
	ModTime time.Time
	Size    int64
}

// Reloader watches the plugin directories and rebuilds the provider set when
// they change.
//
// It owns the lifetime of the provider sets. That is deliberate: an earlier
// version had the caller pass the replaced set to Retire(), and the caller
// retired the set Poll had just *returned* instead of the one going out of use.
// Nothing failed at the call site — the providers stayed fine until the next
// poll, which closed the set the UI was displaying. Owning the lifetime here
// means the only public operation is "give me the new set", and there is no
// slice for the caller to get wrong.
//
// Polling rather than fsnotify is a deliberate choice. The thing being watched
// is a handful of small text files that a human edits, so a one second delay
// is invisible, while an OS watcher brings event coalescing, rename semantics
// and a background goroutine's lifetime to get wrong — on Windows especially.
// If the delay ever becomes noticeable, swapping Poll's body for a watcher does
// not disturb the rest of this type.
type Reloader struct {
	loader *Loader

	// stamps is the state of the world as of the last successful poll.
	stamps map[string]fileStamp
	// started guards the first poll, which must not be reported as a change.
	started bool

	// live is the set the caller currently has installed.
	live []*LuaProvider
	// retired holds sets replaced by a reload. They are closed on the next
	// poll rather than immediately: a benchmark or plan call may still be in
	// flight on the old VM, and State.Close takes the same mutex as
	// CallMethod, so closing at replacement time would stall the UI thread
	// until that call finishes (or times out, ten seconds later).
	retired []*LuaProvider
}

// NewReloader returns a reloader over the same directories the loader reads.
func NewReloader(l *Loader) *Reloader {
	return &Reloader{loader: l}
}

// Adopt records the set the caller loaded at startup as the live one.
//
// Without this the first reload would have nothing to retire, and the set
// loaded at startup — the one the first Poll replaces — would never be closed.
func (r *Reloader) Adopt(ps []*LuaProvider) {
	r.live = append(r.live[:0], ps...)
}

// Snapshot records the current state of the plugin files without loading them.
// It is called once after the initial load so the first Poll does not mistake
// what is already loaded for a pending change.
func (r *Reloader) Snapshot() error {
	stamps, err := r.scan()
	if err != nil {
		return err
	}
	r.stamps = stamps
	r.started = true
	return nil
}

// Poll compares the plugin files against the last snapshot.
//
// When nothing changed it returns changed=false and the caller keeps its
// current providers. When something changed it returns the freshly loaded set
// and hands the previously live set over to the retirement queue; the caller
// only has to swap the new set in.
//
// A plugin that fails to compile after an edit does not abort the reload: the
// broken plugin is reported in diag and the rest still load, so an editor's
// half-saved file does not take the whole list down. When *every* plugin fails
// and nothing was loaded, that is treated as "no usable change", the live set is
// left alone, and the caller is told changed=false so it keeps working with what
// it has.
func (r *Reloader) Poll() (provs []*LuaProvider, diag []PluginSource, changed bool, err error) {
	stamps, err := r.scan()
	if err != nil {
		return nil, nil, false, err
	}

	// The first poll establishes the baseline. Without this guard every start
	// would report a reload of plugins that were just loaded.
	if !r.started {
		r.stamps = stamps
		r.started = true
		return nil, nil, false, nil
	}
	if sameStamps(r.stamps, stamps) {
		r.closeRetired()
		return nil, nil, false, nil
	}

	// Record the new stamps before loading. If loading is slow and the file
	// changes again in the meantime, the next poll must still see a
	// difference rather than assume this load caught up.
	prev := r.stamps
	r.stamps = stamps

	loaded, diag, lerr := r.loader.Load()
	if lerr != nil {
		// The loader could not even enumerate: keep the previous stamps so the
		// next poll retries instead of believing it has seen this state.
		r.stamps = prev
		return nil, diag, false, lerr
	}
	if len(loaded) == 0 {
		// Everything broke. Treat it as "not a usable reload": the caller keeps
		// the set it has, and the diagnostics are still surfaced.
		r.stamps = prev
		if len(diag) == 0 {
			return nil, nil, false, fmt.Errorf("no plugins loaded after change")
		}
		return nil, diag, false, nil
	}

	r.closeRetired()

	// The outgoing set becomes retired and the freshly loaded one becomes
	// live. Rotation lives here rather than at the call site so "which set is
	// going out of use" is never a caller's judgement call.
	r.retired = r.live
	r.live = loaded
	return loaded, diag, true, nil
}

// closeRetired releases the sets that were replaced on a previous reload.
func (r *Reloader) closeRetired() {
	for _, p := range r.retired {
		p.Close()
	}
	r.retired = nil
}

// Close releases everything the reloader still holds. The TUI calls it when the
// program exits, at which point the live set has no more callers.
func (r *Reloader) Close() {
	r.closeRetired()
	for _, p := range r.live {
		p.Close()
	}
	r.live = nil
}

// scan collects a stamp for every .lua file the loader would read.
//
// The user directory overrides the official one by id, so only the winning
// file per id is stamped: editing a shadowed official plugin must not report a
// change that has no effect.
func (r *Reloader) scan() (map[string]fileStamp, error) {
	byID := map[string]string{}

	for _, dir := range []string{r.loader.OfficialPluginDir(), r.loader.UserPluginDir()} {
		if dir == "" {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			// A missing directory is normal: user/ may never be created.
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".lua") {
				continue
			}
			// Later directories win, matching Loader.List's precedence.
			byID[strings.TrimSuffix(e.Name(), ".lua")] = filepath.Join(dir, e.Name())
		}
	}

	out := make(map[string]fileStamp, len(byID))
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		p := byID[id]
		fi, err := os.Stat(p)
		if err != nil {
			// Removed between ReadDir and Stat: record it as absent so the
			// comparison sees a difference and the next poll reloads.
			continue
		}
		out[p] = fileStamp{ModTime: fi.ModTime(), Size: fi.Size()}
	}
	return out, nil
}

// sameStamps reports whether two scans describe the same set of files.
func sameStamps(a, b map[string]fileStamp) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok || !va.ModTime.Equal(vb.ModTime) || va.Size != vb.Size {
			return false
		}
	}
	return true
}
