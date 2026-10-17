package engine

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"keep-mirrors/internal/env"
	"keep-mirrors/internal/paths"

	"github.com/yuin/gopher-lua"
)

// openOsTrimmed opens a reduced os library: only the clock, the date formatter
// and environment reads. Everything that touches the filesystem or spawns a
// process is removed by removeUnsafeGlobals.
func openOsTrimmed(L *lua.LState) int {
	mod := L.RegisterModule("os", map[string]lua.LGFunction{
		"clock":    osClock,
		"date":     osDate,
		"difftime": osDiffTime,
		"getenv":   osGetenv,
		"time":     osTime,
	})
	L.Push(mod)
	return 1
}

// ── os subset ───────────────────────────────────────────────────────────

func osClock(L *lua.LState) int {
	L.Push(lua.LNumber(time.Since(startedAt).Seconds()))
	return 1
}

func osDiffTime(L *lua.LState) int {
	L.Push(lua.LNumber(L.CheckInt64(1) - L.CheckInt64(2)))
	return 1
}

func osTime(L *lua.LState) int {
	if L.GetTop() == 0 {
		L.Push(lua.LNumber(time.Now().Unix()))
		return 1
	}
	L.Push(lua.LNumber(time.Now().Unix()))
	return 1
}

func osDate(L *lua.LState) int {
	// A real implementation would honour the format string; plugins use this
	// only for log timestamps, so the ISO form is enough.
	L.Push(lua.LString(time.Now().UTC().Format("2006-01-02T15:04:05Z")))
	return 1
}

func osGetenv(L *lua.LState) int {
	v := os.Getenv(L.CheckString(1))
	if v == "" {
		L.Push(lua.LNil)
		return 1
	}
	L.Push(lua.LString(v))
	return 1
}

// startedAt is a process-wide reference point for os.clock.
var startedAt = time.Now()

// ── km table ────────────────────────────────────────────────────────────

// bindKM installs the km global. It is the entire plugin-facing API.
func bindKM(L *lua.LState, opts Options) {
	km := L.NewTable()

	L.SetField(km, "platform", L.NewFunction(func(L *lua.LState) int {
		L.Push(lua.LString(platform()))
		return 1
	}))
	L.SetField(km, "home", L.NewFunction(func(L *lua.LState) int {
		L.Push(lua.LString(home()))
		return 1
	}))

	// km.env
	envT := L.NewTable()
	L.SetField(envT, "get", L.NewFunction(func(L *lua.LState) int {
		v := os.Getenv(L.CheckString(1))
		if v == "" {
			L.Push(lua.LNil)
			return 1
		}
		L.Push(lua.LString(v))
		return 1
	}))
	// km.env.persisted reads the durable store, which is what "is this
	// configured" actually means: a variable set in this process but not
	// persisted would vanish on the next terminal.
	L.SetField(envT, "persisted", L.NewFunction(func(L *lua.LState) int {
		v, err := env.Read(L.CheckString(1))
		if err != nil || v == "" {
			L.Push(lua.LNil)
			return 1
		}
		L.Push(lua.LString(v))
		return 1
	}))
	L.SetField(km, "env", envT)

	// km.fs — confined to home, read-only, size-limited.
	fsT := L.NewTable()
	L.SetField(fsT, "read", L.NewFunction(func(L *lua.LState) int {
		p := L.CheckString(1)
		body, ok := readWithinHome(L, p)
		if !ok {
			// readWithinHome either raised (out of bounds) or could not open
			// the file. In both cases the plugin sees nil, which its helpers
			// treat as "no content".
			L.Push(lua.LNil)
			return 1
		}
		L.Push(lua.LString(body))
		return 1
	}))
	L.SetField(fsT, "exists", L.NewFunction(func(L *lua.LState) int {
		p := L.CheckString(1)
		if !withinHome(p) {
			L.Push(lua.LFalse)
			return 1
		}
		if _, err := os.Stat(paths.Expand(p)); err != nil {
			L.Push(lua.LFalse)
			return 1
		}
		L.Push(lua.LTrue)
		return 1
	}))
	// km.fs.which resolves a command on PATH, for detection.
	L.SetField(fsT, "which", L.NewFunction(func(L *lua.LState) int {
		name := L.CheckString(1)
		if p, err := exec.LookPath(name); err == nil {
			L.Push(lua.LString(p))
			return 1
		}
		L.Push(lua.LNil)
		return 1
	}))
	L.SetField(km, "fs", fsT)

	// km.exec — gated on opts.AllowExec and an executable whitelist.
	execT := L.NewTable()
	L.SetField(execT, "run", L.NewFunction(func(L *lua.LState) int {
		if !opts.AllowExec {
			L.RaiseError("km.exec is disabled for this plugin")
			return 0
		}
		tbl := L.CheckTable(1)
		cmdT := tbl.RawGetString("cmd")
		cmds, ok := cmdT.(*lua.LTable)
		if !ok || cmds.Len() == 0 {
			L.RaiseError("km.exec.run: cmd must be a non-empty array")
			return 0
		}
		argv := make([]string, 0, cmds.Len())
		for i := 1; i <= cmds.Len(); i++ {
			argv = append(argv, lua.LVAsString(cmds.RawGetInt(i)))
		}
		if !execAllowed(opts, argv[0]) {
			L.RaiseError("km.exec.run: %q is not in the allowed list", argv[0])
			return 0
		}
		timeout := 5 * time.Second
		if n := int(lua.LVAsNumber(tbl.RawGetString("timeout"))); n > 0 {
			timeout = time.Duration(n) * time.Second
		}
		done := make(chan [2]string, 1)
		go func() {
			out, errOut := runCmd(argv, timeout)
			done <- [2]string{out, errOut}
		}()
		select {
		case r := <-done:
			ret := L.NewTable()
			L.SetField(ret, "stdout", lua.LString(r[0]))
			L.SetField(ret, "stderr", lua.LString(r[1]))
			L.Push(ret)
			return 1
		case <-time.After(timeout + time.Second):
			L.RaiseError("km.exec.run: timed out after %s", timeout)
			return 0
		}
	}))
	L.SetField(km, "exec", execT)

	// km.http — our own client, so the plugin never sees a socket.
	httpT := L.NewTable()
	L.SetField(httpT, "get", L.NewFunction(func(L *lua.LState) int {
		if !opts.AllowHTTP {
			L.RaiseError("km.http is disabled for this plugin")
			return 0
		}
		url := L.CheckString(1)
		if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") {
			L.RaiseError("km.http.get: only http(s) URLs are allowed")
			return 0
		}
		timeout := 5 * time.Second
		if tbl, ok := L.Get(2).(*lua.LTable); ok {
			if n := int(lua.LVAsNumber(tbl.RawGetString("timeout"))); n > 0 {
				timeout = time.Duration(n) * time.Second
			}
		}
		client := &http.Client{Timeout: timeout}
		resp, err := client.Get(url)
		if err != nil {
			L.Push(lua.LNil)
			L.Push(lua.LString(err.Error()))
			return 2
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		ret := L.NewTable()
		L.SetField(ret, "status", lua.LNumber(resp.StatusCode))
		L.SetField(ret, "body", lua.LString(string(body)))
		L.Push(ret)
		return 1
	}))
	L.SetField(km, "http", httpT)

	// km.log — the plugin's only output channel.
	L.SetField(km, "log", L.NewFunction(func(L *lua.LState) int {
		level := L.CheckString(1)
		msg := L.CheckString(2)
		logf("[%s] %s", level, msg)
		return 0
	}))

	L.SetGlobal("km", km)
}

// readWithinHome reads a file if it is inside the home directory.
//
// It returns ok=false when the path escapes home (after raising a Lua error,
// which unwinds the plugin call) or when the file cannot be opened. The
// caller pushes nil in both cases, so this function never touches the Lua
// stack itself: mixing a push here with the caller's push would desynchronise
// the value stack, which shows up as a plugin receiving garbage.
func readWithinHome(L *lua.LState, p string) (string, bool) {
	if !withinHome(p) {
		L.RaiseError("km.fs.read: %q is outside the home directory", p)
		return "", false
	}
	f, err := os.Open(paths.Expand(p))
	if err != nil {
		return "", false
	}
	defer f.Close()
	body, _ := io.ReadAll(io.LimitReader(f, 4<<20))
	return string(body), true
}

// withinHome is the containment check for anything a plugin asks us to open.
//
// It resolves symlinks before comparing, not just lexically: a link inside home
// pointing outside it is a real escape, and the lexical check would happily
// approve it because the *name* starts with home.
func withinHome(p string) bool {
	return paths.WithinHomeResolved(p)
}

// execAllowed checks the whitelist. An empty whitelist means "any executable
// found on PATH", which is only used for built-in plugins we ship ourselves.
func execAllowed(opts Options, cmd string) bool {
	if len(opts.AllowedExec) == 0 {
		return true
	}
	base := filepath.Base(cmd)
	base = strings.TrimSuffix(base, ".exe")
	base = strings.TrimSuffix(base, ".cmd")
	base = strings.TrimSuffix(base, ".bat")
	for _, a := range opts.AllowedExec {
		if a == base || a == cmd {
			return true
		}
	}
	return false
}

// runCmd runs argv with a timeout and returns trimmed stdout and stderr.
func runCmd(argv []string, timeout time.Duration) (string, string) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return strings.TrimSpace(out.String()), "timed out"
	}
	if err != nil {
		return strings.TrimSpace(out.String()), strings.TrimSpace(errb.String())
	}
	return strings.TrimSpace(out.String()), strings.TrimSpace(errb.String())
}

// pluginLog is where km.log writes. It defaults to stderr so a misbehaving
// plugin is visible during development.
var pluginLog io.Writer = os.Stderr

// logf writes to the plugin log sink.
func logf(format string, args ...any) {
	fmt.Fprintf(pluginLog, format+"\n", args...)
}
