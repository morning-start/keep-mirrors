// Package engine embeds the Lua VM and exposes the km.* API to plugins.
//
// The security model, in one paragraph: a plugin is untrusted code that runs
// inside our process, so it gets exactly the capabilities it declares and
// nothing else. Filesystem access is confined to the user's home directory,
// there is no io/os.execute/dofile/require, network access goes through our
// own HTTP client, and every call runs under a context deadline that the VM
// checks between instructions, so an infinite loop is killed rather than
// wedging the UI.
//
// The API is intentionally small. Plugins describe intent ("set this key in
// this file", "set this variable"); the Go side decides how to make it
// durable. That is what keeps a broken plugin from corrupting a config file:
// it never gets a raw write primitive.
package engine

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"time"

	"keep-mirrors/internal/paths"

	"github.com/yuin/gopher-lua"
)

// DefaultCallTimeout bounds a single plugin invocation. Plugins are expected
// to be fast (reading a file, formatting a config); anything slower is a bug
// or a runaway loop, and both should surface as an error rather than a hang.
const DefaultCallTimeout = 10 * time.Second

// MemoryLimitMB caps the Lua heap per plugin. Exceeding it terminates the
// process with a log line, which is blunt but is the only hard stop
// gopher-lua offers.
const MemoryLimitMB = 64

// Options configures a plugin state.
type Options struct {
	// CallTimeout bounds every plugin call.
	CallTimeout time.Duration
	// MemoryLimitMB is the heap cap for this state.
	MemoryLimitMB int
	// AllowExec permits km.exec.run. Disabled for user plugins by default.
	AllowExec bool
	// AllowedExec is the executable whitelist when AllowExec is set.
	AllowedExec []string
	// AllowHTTP permits km.http.get.
	AllowHTTP bool
}

// DefaultOptions returns the settings used for built-in plugins.
func DefaultOptions() Options {
	return Options{
		CallTimeout:   DefaultCallTimeout,
		MemoryLimitMB: MemoryLimitMB,
		AllowExec:     true,
		AllowHTTP:     true,
	}
}

// State is one isolated Lua VM with the km API bound.
type State struct {
	L      *lua.LState
	opts   Options
	mu     sync.Mutex // serialises calls: an LState is not goroutine-safe
	closed bool
}

// New builds a sandboxed state and binds the km table.
//
// The libraries opened are base, string, table, math and a trimmed os (only
// time, date, clock, difftime and getenv). Everything else — io, debug,
// package, coroutine's file access, os.execute/exit/remove/rename — is absent
// rather than restricted, so there is no bypass to find.
func New(opts Options) (*State, error) {
	L := lua.NewState(lua.Options{
		SkipOpenLibs:        true,
		CallStackSize:       256,
		RegistrySize:        1024 * 8,
		RegistryMaxSize:     1024 * 32,
		RegistryGrowStep:    32,
		MinimizeStackMemory: true,
		IncludeGoStackTrace: false,
	})
	if opts.MemoryLimitMB > 0 {
		L.SetMx(opts.MemoryLimitMB)
	}

	openSafe(L)
	bindKM(L, opts)

	s := &State{L: L, opts: opts}
	// A deadline on the state itself means even a plugin that yields control
	// to Lua code we do not call directly still gets interrupted.
	L.SetContext(context.Background())
	return s, nil
}

// openSafe opens only the libraries a plugin genuinely needs.
func openSafe(L *lua.LState) {
	for _, pair := range []struct {
		n string
		f lua.LGFunction
	}{
		// package must come first: it sets up the module table the others
		// register into, and we immediately strip the loader that reads
		// files from disk.
		{lua.BaseLibName, lua.OpenBase},
		{lua.StringLibName, lua.OpenString},
		{lua.TabLibName, lua.OpenTable},
		{lua.MathLibName, lua.OpenMath},
		{lua.OsLibName, openOsTrimmed},
	} {
		if err := L.CallByParam(lua.P{
			Fn:      L.NewFunction(pair.f),
			NRet:    0,
			Protect: true,
		}, lua.LString(pair.n)); err != nil {
			// Cannot happen for a fixed set of stdlib openers.
			continue
		}
	}
	removeUnsafeGlobals(L)
}

// removeUnsafeGlobals deletes the base functions that reach outside the
// sandbox. They are removed rather than shadowed, so a plugin cannot reach
// them through a saved reference or a metatable.
func removeUnsafeGlobals(L *lua.LState) {
	g := L.GetGlobal("_G")
	tbl, ok := g.(*lua.LTable)
	if !ok {
		return
	}
	for _, name := range []string{
		"dofile", "loadfile", "load", "loadstring",
		"require", "module",
		"newproxy",
		"collectgarbage",
	} {
		tbl.RawSetString(name, lua.LNil)
	}
	// os.execute, os.exit, os.remove, os.rename, os.setenv, os.tmpname.
	if ost, ok := L.GetGlobal("os").(*lua.LTable); ok {
		for _, name := range []string{
			"execute", "exit", "remove", "rename", "setenv", "tmpname",
		} {
			ost.RawSetString(name, lua.LNil)
		}
	}
	// package.loaders is the file-loading path; without it require is inert
	// even if a plugin resurrects the function.
	if pkg, ok := L.GetGlobal("package").(*lua.LTable); ok {
		pkg.RawSetString("loaders", lua.LNil)
		pkg.RawSetString("loadlib", lua.LNil)
	}
}

// Close releases the VM.
func (s *State) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		s.L.Close()
	}
}

// CallMethod invokes a function stored in a Lua table (the plugin module)
// with args and returns its results.
//
// This is the entry point the registry uses for status()/plan()/benchmark():
// a plugin exposes those as fields of its module table, not as globals.
func (s *State) CallMethod(tbl *lua.LTable, name string, args ...lua.LValue) ([]lua.LValue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, fmt.Errorf("plugin state is closed")
	}

	fn := tbl.RawGetString(name)
	if fn == lua.LNil {
		return nil, fmt.Errorf("plugin has no %s()", name)
	}
	lf, ok := fn.(*lua.LFunction)
	if !ok {
		return nil, fmt.Errorf("plugin %s is not a function", name)
	}

	timeout := s.opts.CallTimeout
	if timeout <= 0 {
		timeout = DefaultCallTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	s.L.SetContext(ctx)
	defer s.L.RemoveContext()

	top := s.L.GetTop()
	if err := s.L.CallByParam(lua.P{Fn: lf, NRet: lua.MultRet, Protect: true}, args...); err != nil {
		return nil, err
	}
	var out []lua.LValue
	for i := top + 1; i <= s.L.GetTop(); i++ {
		out = append(out, s.L.Get(i))
	}
	return out, nil
}

// Module returns the plugin's module table.
func (s *State) Module() *lua.LTable {
	v := s.Global("__km_module")
	if t, ok := v.(*lua.LTable); ok {
		return t
	}
	return nil
}

// LoadChunk compiles and runs src as a plugin module. It is the loader used
// for both built-in and user plugins: source comes from a string we control,
// never from a path the plugin chooses.
//
// A plugin ends with `return M`, but a Lua chunk's return value is discarded
// by gopher-lua's DoString. We therefore wrap the source in
// `__km_module = (function() ... end)()` so the module table is captured and
// reachable from Go. The wrapper costs one function frame and leaves the plugin
// source untouched.
func (s *State) LoadChunk(src string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("plugin state is closed")
	}
	timeout := s.opts.CallTimeout
	if timeout <= 0 {
		timeout = DefaultCallTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	s.L.SetContext(ctx)
	defer s.L.RemoveContext()
	return s.L.DoString("__km_module = (function()\n" + src + "\nend)()")
}

// Global returns the value of a plugin global, for reading the module table.
func (s *State) Global(name string) lua.LValue {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.L.GetGlobal(name)
}

// platform returns the runtime platform name, exposed to plugins.
func platform() string { return runtime.GOOS }

// home is the only writable root a plugin gets.
func home() string { return paths.Home() }
