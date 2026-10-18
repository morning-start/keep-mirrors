# Plugin Contract v1

A plugin is a Lua file that returns a module table. Put it in
`<appdata>/keep-mirrors/plugins/user/` (yours; a same-named file overrides the
official one) or `official/` (curated, rewritten by sync). `km plugin dir`
prints both paths.

Plugins are **not compiled into the binary**. They are read from disk at
runtime, so editing one changes behaviour on the next run without a rebuild.

A plugin **never writes a file directly**. It declares intent and Go performs
the write. That is the whole design: backup, diff, undo, atomic replace and
permission preservation all live on the Go side, so a buggy plugin cannot
corrupt a config file.

## Metadata

The first group is what the tool needs in order to run the plugin. The second
group is published information, read when someone asks about a plugin — it is
never consulted while rendering the list.

```lua
local M = {}

-- Required: identity and contract
M.api       = { major = 1, minor = 0 }   -- contract version; a major mismatch is refused
M.id        = "pip"                       -- stable id, also the managed-block name
M.title     = "Python / pip"              -- display name
M.category  = "language"                  -- language|container|os|tool

-- Expected of anything distributed publicly
M.version     = "1.0.0"                   -- the plugin's own release
M.author      = "keep-mirrors"
M.license     = "MIT"
M.homepage    = "https://github.com/morning-start/keep-mirrors/blob/plugins/pip.lua"
M.description = "pip 的 index-url 与 trusted-host"

-- Advisory: used by the UI, not a load-time gate
M.platforms = { "windows", "linux", "darwin" }
M.detects   = { "python", "python3", "pip" }  -- used to detect installation
M.envs      = { "PIP_INDEX_URL" }         -- environment variables this source touches
```

**`version` and `api` are different axes** and must not be conflated: changing
a mirror URL bumps `version`, while using a new `km.*` capability bumps `api`.
Only `api` gates loading.

Bump `version` whenever you change a plugin, even a URL. It is what a bug
report quotes, and what tells a user whether the copy on their disk is the one
they meant to install.

## Presets and settings

```lua
M.presets = {
  { id = "tuna", label = "Tsinghua TUNA",
    opts = { index = "https://pypi.tuna.tsinghua.edu.cn/simple" } },
  { id = "official", label = "Official",
    opts = { index = "" } },              -- empty value = restore the official source
}

M.settings = {
  { key = "index", type = "string", label = "Index URL",
    default = "", validate = "url", help = "Leave empty to restore the official source" },
  { key = "trusted_host", type = "bool", label = "Write trusted-host",
    default = true },
  { key = "sumdb", type = "choice", label = "GOSUMDB",
    default = "sum.golang.google.cn",
    choices = { "sum.golang.google.cn", "sum.golang.org", "off" } },
}
```

**Preset declaration order matters**: the first preset is the default when the
user never touches the picker. Do not sort presets by id.

## The three functions

```lua
-- Current state; the list view uses it for badges.
function M.status(ctx)
  return { installed = true, current = "https://...", healthy = true, note = "" }
end

-- Build the change plan (pure function, executes nothing).
function M.plan(ctx, opts)
  return { changes = { ... }, warnings = { "..." } }
end

-- Optional: benchmark.
function M.benchmark(ctx, urls)
  return { { name = "tuna", url = "https://...", ms = 120, ok = true, err = nil } }
end
```

## The four change kinds

```lua
-- 1. Rewrite a config file (managed block)
{ kind = "config",
  file   = "~/.pip/pip.conf",     -- ~ prefix or absolute path
  format = "hash",                -- hash|semicolon|slash|xml   comment style
  syntax = "ini",                 -- ini|toml|kv|yaml           value syntax
  block  = "pip",                 -- managed-block name, usually M.id
  entries = {
    { section = "global", key = "index-url", value = "https://..." },
    { section = "",        key = "registry",   value = "https://..." },  -- no section
  } }

-- 2. Remove the managed block entirely (restore-official path)
{ kind = "config-remove",
  file   = "~/.pip/pip.conf",
  format = "hash",
  block  = "pip" }

-- 3. Set an environment variable
{ kind = "env", name = "PIP_INDEX_URL", value = "https://..." }

-- 4. Remove an environment variable
{ kind = "env-unset", name = "PIP_INDEX_URL" }
```

**Restoring the official source is `config-remove`, never an empty value.**
Writing `index-url = ` or `registry=` leaves the tool with an invalid config,
not its default: npm fails every install with `ERR_INVALID_URL` on a blank
registry, and cargo cannot resolve crates.io when `replace-with = ''` (it
looks for a source literally named `""`). Deleting the managed block is what
actually returns the tool to its built-in behaviour.

**Read-back patterns must not cross lines.** `%s` matches newline in Lua, so
`registry%s*=` eats the line break after an empty value and the capture lands
on the next line — historically the `# <<<` sentinel, which status then
reported as the configured registry. Use `[ \t]*` for the padding around `=`
and `[^\n]*` (not `+`) for the value, so an empty value matches as empty.

### Rendering rules per syntax

| syntax | rendered as | used by |
|---|---|---|
| `ini` | `key = value` (unquoted) | pip.conf |
| `toml` | `key = "value"` (auto-quoted) | cargo/config.toml |
| `kv` | `key=value` (no sections) | .npmrc, go env |
| `yaml` | `  key: value` | YAML config |

A value that already carries quotes (for example the TOML literal string
`'keep-mirrors'`) is emitted verbatim, never double-quoted.

## The km API

```lua
km.platform()                        -- "windows" | "linux" | "darwin"
km.home()                            -- user home directory

km.env.get("NAME")                   -- read this process's env; nil when unset
km.env.persisted("NAME")             -- read the durable store (registry / profile)

km.fs.read(path)                     -- read a text file under ~; nil when absent
km.fs.exists(path)                   -- exists under ~
km.fs.which("python")                -- find an executable on PATH

km.exec.run({ cmd = {"npm","config","get","registry"}, timeout = 5 })  -- -> { stdout, stderr }
km.http.get(url, { timeout = 8 })    -- -> { status, body } or nil, err

km.log("info", "message")            -- write to the plugin log
os.clock(), os.date()                -- timing; os.time/difftime/getenv also exist
```

All paths resolve relative to `km.home()`. `km.fs.read` refuses paths outside
`~` by raising an error.

**Resource caps** (enforced, not advisory): `km.http.get` truncates the body
at 1 MiB and `km.fs.read` at 4 MiB — do not fetch anything larger and expect
the tail. `km.exec.run` and `km.http.get` default to a 5 s timeout; pass
`timeout = n` (seconds) to override. Every Lua call is additionally capped at
10 s by the engine, which kills the VM — the per-call timeout only bounds the
child process / HTTP request inside it.

## Reading back a managed block

`status()` needs to know what is currently configured. Use a plain find and
**never the Lua pattern `%-`**:

```lua
-- correct: plain find; '-' is not a metacharacter outside a character class
local s = body:find(">>> keep-mirrors:pip >>>", 1, true)

-- wrong: '%-' is a lazy quantifier, so the search silently never matches
local s = body:find(">>> keep%-mirrors:pip >>>", 1, true)
```

## Sandbox limits

Unavailable: `io.*`, `os.execute`, `os.exit`, `os.remove`, `os.rename`,
`os.setenv`, `dofile`, `loadfile`, `load`, `loadstring`, `require`, `debug`,
`collectgarbage`.

Available: `base`, `string`, `table`, `math`, `os.time`, `os.date`,
`os.clock`, `os.difftime`, `os.getenv`.

Every call has a 10 second timeout and is killed when it expires. Memory is
capped at 64 MB.

## Troubleshooting

| Symptom | Cause |
|---|---|
| Plugin does not appear | `M.id` missing, or `api.major` is not 1 |
| A user plugin did not override the official one | the file must be named `<id>.lua`, and `id` must match the official plugin's id exactly |
| Edits had no effect | you edited the seed in the source repo; the running copy lives in `<appdata>/keep-mirrors/plugins/` |
| Edits keep being reverted | you edited `official/`; copy the file into `user/` and edit that instead |
| `status()` reports "not a function" | a `local function` is referenced before it is declared; Lua locals must be declared before use |
| `status()` always returns empty | usually `%-` written where `-` was meant (see above) |
| Writes to an unexpected directory | `CARGO_HOME` / `GOENV` point elsewhere; validate with `km.fs.exists()` first |
