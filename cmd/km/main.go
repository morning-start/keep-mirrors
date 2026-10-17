// Command km is a terminal UI for managing package-manager mirror sources.
//
// By default it opens the interactive interface. Every mirror source is
// implemented as a Lua plugin (see the plugins directory), loaded from a
// sandbox that confines them to the user's home directory and gives them a
// declarative API: a plugin proposes changes, the Go core applies them with a
// backup taken first.
//
// Usage:
//
//	km                  open the TUI
//	km list             list providers and their status
//	km plan <id>        show what a preset would change, without applying
//	km apply <id>       apply a preset (--preset to choose, --yes to skip the prompt)
//	km bench <id>       measure every preset endpoint
//	km undo             restore the most recent backup
//	km plugin list      list the loaded plugins
//	km plugin show <id> print a plugin's metadata
package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"keep-mirrors/internal/app"
	"keep-mirrors/internal/core"
	"keep-mirrors/internal/engine"
	"keep-mirrors/internal/plugins"
	"keep-mirrors/internal/provider"
	"keep-mirrors/internal/registry"
	"keep-mirrors/internal/tui"
)

// pluginSeed is the embedded snapshot the loader unpacks on first run.
//
// Plugins are read from disk at runtime, never from here. This snapshot only
// fills official/ when it is empty, which keeps a fresh install usable offline
// without turning the binary into a second source of truth.
var pluginSeed = plugins.FS

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "km: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	engine.SetSeed(pluginSeed)

	if len(args) == 0 {
		return runTUI()
	}

	switch args[0] {
	case "list", "ls":
		return cmdList(args[1:])
	case "plan":
		return cmdPlan(args[1:])
	case "apply":
		return cmdApply(args[1:])
	case "bench":
		return cmdBench(args[1:])
	case "undo":
		return cmdUndo(args[1:])
	case "plugin":
		return cmdPlugin(args[1:])
	case "backups":
		return cmdBackups(args[1:])
	case "help", "-h", "--help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

// newLoader builds a loader with the seed wired in and makes sure the official
// directory exists.
//
// Seeding is idempotent and only fires when official/ is empty, so calling it
// from every entry point is safe: a second run never overwrites a synced or
// hand-edited plugin set.
func newLoader() *engine.Loader {
	loader := engine.NewLoader()
	if n, err := loader.SeedIfEmpty(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not seed plugins: %v\n", err)
	} else if n > 0 {
		fmt.Fprintf(os.Stderr, "seeded %d plugin(s) into %s\n", n, loader.OfficialPluginDir())
	}
	return loader
}

// loadRegistry compiles every plugin and builds the provider registry.
func loadRegistry() (*registry.Registry, []engine.PluginSource, error) {
	provs, diag, err := newLoader().Load()
	if err != nil {
		return nil, diag, err
	}
	reg := registry.New()
	for _, p := range provs {
		reg.Register(p)
	}
	return reg, diag, nil
}

// ── commands ───────────────────────────────────────────────────────────

func runTUI() error {
	reg, diag, err := loadRegistry()
	if err != nil {
		return err
	}
	reportBroken(diag)

	m := app.New(reg)
	m.RefreshSelection()
	return tui.Run(m)
}

func cmdList(args []string) error {
	reg, diag, err := loadRegistry()
	if err != nil {
		return err
	}
	reportBroken(diag)

	m := app.New(reg)
	for _, cat := range reg.Categories() {
		fmt.Printf("%s\n", categoryName(cat))
		for _, p := range reg.ByCategory()[cat] {
			st := p.Status(m.Ctx())
			fmt.Printf("  %-16s %-8s %s\n", p.ID(), badge(st), short(st.Current, 60))
		}
	}
	return nil
}

// badge renders the two independent facts a provider reports: whether its
// toolchain is present, and whether a mirror is configured. They are not
// exclusive — a configured pip whose interpreter is not on PATH is both — so
// "configured" wins, since that is what the user is looking at.
func badge(st provider.Status) string {
	switch {
	case st.Current != "":
		return "● 已配置"
	case st.Installed:
		return "○ 未配置"
	default:
		return "○ 未安装"
	}
}

func cmdPlan(args []string) error {
	id, preset, _, err := parseProvider(args)
	if err != nil {
		return err
	}
	reg, _, err := loadRegistry()
	if err != nil {
		return err
	}
	p, ok := reg.Get(id)
	if !ok {
		return registry.ErrNoProvider(id)
	}

	m := app.New(reg)
	opts, pid, err := resolvePreset(p, preset)
	if err != nil {
		return err
	}
	plan, err := p.Plan(m.Ctx(), withPreset(opts, pid))
	if err != nil {
		return err
	}
	plan.PresetID = pid
	if err := plan.Describe(); err != nil {
		return err
	}
	if plan.Empty() {
		fmt.Println("no changes")
		return nil
	}
	for _, c := range plan.Changes {
		fmt.Println(core.Diff(c))
	}
	for _, w := range plan.Warnings {
		fmt.Printf("warning: %s\n", w)
	}
	return nil
}

func cmdApply(args []string) error {
	id, preset, yes, err := parseProvider(args)
	if err != nil {
		return err
	}
	reg, _, err := loadRegistry()
	if err != nil {
		return err
	}
	p, ok := reg.Get(id)
	if !ok {
		return registry.ErrNoProvider(id)
	}

	m := app.New(reg)
	opts, pid, err := resolvePreset(p, preset)
	if err != nil {
		return err
	}
	plan, err := p.Plan(m.Ctx(), withPreset(opts, pid))
	if err != nil {
		return err
	}
	plan.PresetID = pid
	if err := plan.Describe(); err != nil {
		return err
	}
	if plan.Empty() {
		fmt.Println("nothing to apply")
		return nil
	}

	fmt.Printf("Applying %s/%s:\n\n", id, labelOfID(p, pid))
	for _, c := range plan.Changes {
		fmt.Println(core.Diff(c))
	}
	fmt.Println()

	if !yes && !confirm("apply these changes?") {
		fmt.Println("aborted")
		return nil
	}

	bak, err := core.Apply(plan)
	if err != nil {
		return err
	}
	fmt.Printf("done. backup: %s\n", backupID(bak))
	fmt.Println("undo with: km undo")
	return nil
}

func cmdBench(args []string) error {
	id, _, _, err := parseProvider(args)
	if err != nil {
		return err
	}
	reg, _, err := loadRegistry()
	if err != nil {
		return err
	}
	p, ok := reg.Get(id)
	if !ok {
		return registry.ErrNoProvider(id)
	}

	urls := app.BenchURLs(p)
	if len(urls) == 0 {
		return fmt.Errorf("%s has no benchmarkable presets", id)
	}

	fmt.Printf("measuring %d endpoint(s) for %s...\n", len(urls), id)
	res, err := p.Benchmark(app.New(reg).Ctx(), urls)
	if err != nil {
		return err
	}
	sort.SliceStable(res, func(i, j int) bool {
		if res[i].OK != res[j].OK {
			return res[i].OK
		}
		return res[i].MS < res[j].MS
	})
	for _, r := range res {
		if r.OK {
			fmt.Printf("  %-28s %5d ms\n", r.Name, r.MS)
		} else {
			fmt.Printf("  %-28s %s\n", r.Name, r.Err)
		}
	}
	return nil
}

func cmdUndo(args []string) error {
	list, err := core.ListBackups()
	if err != nil {
		return err
	}
	if len(list) == 0 {
		return fmt.Errorf("no backups to restore")
	}
	b := list[0]
	fmt.Printf("restoring backup %s (%s/%s, %d file(s))\n",
		b.ID, b.Provider, b.PresetID, len(b.Files))
	if err := b.Restore(); err != nil {
		return err
	}
	fmt.Println("done")
	return nil
}

func cmdBackups(args []string) error {
	list, err := core.ListBackups()
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Println("no backups")
		return nil
	}
	for _, b := range list {
		fmt.Printf("%s  %-10s %-12s %2d file(s)\n",
			b.ID, b.Provider, b.PresetID, len(b.Files))
	}
	return nil
}

func cmdPlugin(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("plugin requires a subcommand: list | show <id> | dir")
	}
	loader := newLoader()
	switch args[0] {
	case "dir":
		// The directories are printed unconditionally, including ones that do
		// not exist yet: telling a user where to put a plugin is more useful
		// than reporting only what already exists.
		fmt.Printf("official  %s\n", loader.OfficialPluginDir())
		fmt.Printf("user      %s\n", loader.UserPluginDir())
		return nil

	case "list":
		for _, s := range loader.List() {
			fmt.Printf("%-12s %-9s %s\n", s.ID, s.Origin, s.Path)
		}
		return nil

	case "show":
		if len(args) < 2 {
			return fmt.Errorf("plugin show requires an id")
		}
		p, _, err := loader.LoadOne(args[1])
		if err != nil {
			return err
		}
		defer p.Close()

		m := p.Meta()
		fmt.Printf("id          %s\n", p.ID())
		fmt.Printf("title       %s\n", p.Title())
		fmt.Printf("category    %s\n", p.Category())
		fmt.Printf("version     %s\n", orDash(m.Version))
		fmt.Printf("api         %d.%d\n", m.API.Major, m.API.Minor)
		fmt.Printf("presets")
		for _, pr := range p.Presets() {
			fmt.Printf(" %s", pr.ID)
		}
		fmt.Println()
		fmt.Printf("settings")
		for _, s := range p.Settings() {
			fmt.Printf(" %s", s.Key)
		}
		fmt.Println()

		// Everything below is supplementary: read when someone asks about a
		// plugin, never while rendering the list.
		fmt.Println()
		fmt.Printf("author      %s\n", orDash(m.Author))
		fmt.Printf("license     %s\n", orDash(m.License))
		fmt.Printf("homepage    %s\n", orDash(m.Homepage))
		fmt.Printf("description %s\n", orDash(m.Description))
		fmt.Printf("platforms   %s\n", orDash(strings.Join(m.Platforms, " ")))
		fmt.Printf("detects     %s\n", orDash(strings.Join(m.Detects, " ")))
		fmt.Printf("envs        %s\n", orDash(strings.Join(m.Envs, " ")))
		return nil

	default:
		return fmt.Errorf("unknown plugin subcommand %q", args[0])
	}
}

// orDash renders an empty metadata field as "-" so the output columns line up
// whether or not a plugin filled everything in.
func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

// ── helpers ────────────────────────────────────────────────────────────

// reportBroken prints plugins that failed to load. Silent failure would make
// a typo in a plugin invisible.
func reportBroken(diag []engine.PluginSource) {
	for _, d := range diag {
		if d.Err != nil {
			fmt.Fprintf(os.Stderr, "warning: plugin %s failed to load: %v\n", d.ID, d.Err)
		}
	}
}

// parseProvider extracts the provider id and flags from a command line.
//
// Both flag forms are accepted: "--preset=tuna" and "--preset tuna". The
// space-separated form needs the next argument consumed as the value, which is
// why this walks an index rather than ranging over args.
func parseProvider(args []string) (id, preset string, yes bool, err error) {
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--yes" || a == "-y":
			yes = true
		case strings.HasPrefix(a, "--preset="):
			preset = strings.TrimPrefix(a, "--preset=")
		case a == "--preset":
			if i+1 >= len(args) {
				return "", "", yes, fmt.Errorf("--preset requires a value")
			}
			i++
			preset = args[i]
		default:
			rest = append(rest, a)
		}
	}
	if len(rest) == 0 {
		return "", "", yes, fmt.Errorf("a provider id is required")
	}
	return rest[0], preset, yes, nil
}

// resolvePreset returns the options and id of the preset to use. An empty
// preset argument means "the first one", which is what the TUI preselects, so
// the headless path and the interactive path agree on the default.
//
// An unknown preset id is an error rather than a silent fallback: applying the
// default while the user believes their chosen preset is in effect would be
// worse than refusing.
func resolvePreset(p provider.Provider, preset string) (opts map[string]any, id string, err error) {
	presets := p.Presets()
	if len(presets) == 0 {
		if preset != "" {
			return nil, "", fmt.Errorf("%s has no presets", p.ID())
		}
		return map[string]any{}, "", nil
	}
	if preset == "" {
		return presets[0].Opts, presets[0].ID, nil
	}
	pr := provider.PresetByID(presets, preset)
	if pr == nil {
		return nil, "", fmt.Errorf("%s has no preset %q (available: %s)",
			p.ID(), preset, presetIDs(p))
	}
	return pr.Opts, pr.ID, nil
}

// withPreset stamps the resolved preset id into the options. The plugin
// ignores "_preset"; core records it in the backup manifest and the status bar
// so an undo can say which preset it is rolling back.
func withPreset(opts map[string]any, id string) map[string]any {
	out := make(map[string]any, len(opts)+1)
	for k, v := range opts {
		out[k] = v
	}
	if id != "" {
		out["_preset"] = id
	}
	return out
}

// presetIDs renders the available preset ids for an error message.
func presetIDs(p provider.Provider) string {
	if len(p.Presets()) == 0 {
		return "none"
	}
	ids := make([]string, 0, len(p.Presets()))
	for _, pr := range p.Presets() {
		ids = append(ids, pr.ID)
	}
	return strings.Join(ids, ", ")
}

// labelOfID returns the display label of the preset with the given id, falling
// back to the id itself when the plugin declared none.
func labelOfID(p provider.Provider, id string) string {
	if pr := provider.PresetByID(p.Presets(), id); pr != nil {
		return pr.Label
	}
	return id
}

func categoryName(c provider.Category) string {
	switch c {
	case provider.CategoryLanguage:
		return "语言"
	case provider.CategoryContainer:
		return "容器"
	case provider.CategoryOS:
		return "操作系统"
	default:
		return "工具"
	}
}

// short truncates s to at most n runes. It counts runes rather than bytes so a
// Chinese label is not cut mid-character.
func short(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

func backupID(b *core.Backup) string {
	if b == nil {
		return "none"
	}
	return b.ID
}

func confirm(prompt string) bool {
	fmt.Printf("%s [y/N] ", prompt)
	var answer string
	fmt.Scanln(&answer)
	return strings.HasPrefix(strings.ToLower(answer), "y")
}

func usage() {
	fmt.Print(`km — mirror manager

usage:
  km                    open the TUI
  km list               list providers and their status
  km plan <id>          show what a preset would change
  km apply <id>         apply a preset
  km bench <id>         measure every preset endpoint
  km undo               restore the most recent backup
  km backups            list backups
  km plugin list        list loaded plugins
  km plugin show <id>   print a plugin's metadata

flags for apply:
  --preset=<id>         choose the preset (default: the first one)
  --yes, -y             skip the confirmation prompt
`)
}
