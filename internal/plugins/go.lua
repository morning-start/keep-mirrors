-- go plugin for keep-mirrors.
--
-- Contract v1.0. See docs/plugin-contract.md for the full reference.
--
-- The Go toolchain persists settings in $GOENV (default
-- ~/.config/go/env). We write that file directly rather than shelling out to
-- `go env -w`, so the value we wrote is exactly the value we can read back,
-- and the write is atomic like every other file the tool touches.

local M = {}

M.api       = { major = 1, minor = 0 }
M.id        = "go"
M.version   = "1.0.0"
M.title     = "Go"
M.category  = "language"
M.author    = "keep-mirrors"
M.license   = "MIT"
M.homepage  = "https://github.com/morning-start/keep-mirrors/blob/master/internal/plugins/go.lua"
M.description = "GOPROXY 与 GOSUMDB，直接写 $GOENV 文件"
M.platforms = { "windows", "linux", "darwin" }
M.detects   = { "go" }
M.envs      = { "GOPROXY", "GOSUMDB" }

M.presets = {
  { id = "goproxy.cn", label = "goproxy.cn（七牛）",
    opts = { proxy = "https://goproxy.cn" } },
  { id = "aliyun",     label = "阿里云",
    opts = { proxy = "https://mirrors.aliyun.com/goproxy/" } },
  { id = "goproxy.io", label = "goproxy.io",
    opts = { proxy = "https://goproxy.io" } },
  { id = "official",   label = "官方源",
    opts = { proxy = "" } },
}

M.settings = {
  { key = "proxy", type = "string", label = "GOPROXY 地址",
    default = "", validate = "url",
    help = "留空则恢复官方源" },
  { key = "sumdb", type = "choice", label = "GOSUMDB",
    default = "sum.golang.google.cn",
    choices = { "sum.golang.google.cn", "sum.golang.org", "off" },
    help = "模块校验和数据库" },
  { key = "fallback", type = "bool", label = "追加 direct 回退",
    default = true,
    help = "镜像不可用时直接拉取源码" },
}

-- ── helpers ────────────────────────────────────────────────────────────

local function goenv_path()
  local g = km.env.get("GOENV")
  if g and g ~= "" then return g end
  return km.home() .. "/.config/go/env"
end

local function trim_slash(s)
  return (s:gsub("/+$", ""))
end

local function trim(s)
  return (s:gsub("^%s*(.-)%s*$", "%1"))
end

local function host_of(url)
  local h = url:gsub("^https?://", "")
  return (h:gsub("/.*$", ""))
end

-- The sentinel is located with a plain (non-pattern) find: '-' is not a Lua
-- pattern metacharacter outside a character class, and writing '%-' would
-- make the match lazy so it would silently never match.
local function current_entries(path)
  local body = km.fs.read(path)
  if not body then return nil end
  local s = body:find(">>> keep-mirrors:go >>>", 1, true)
  if not s then return nil end
  local e = body:find("<<< keep-mirrors:go <<<", s, true)
  if not e then return nil end
  return body:sub(s, e)
end

local function current_proxy()
  local v = km.env.get("GOPROXY")
  if v then return v end
  local blk = current_entries(goenv_path())
  if blk then
    local r = blk:match("GOPROXY%s*=%s*([^\n]+)")
    if r then return trim(r) end
  end
  return nil
end

local function go_present()
  for _, c in ipairs(M.detects) do
    if km.fs.which(c) then return true end
  end
  return false
end

-- ── contract functions ─────────────────────────────────────────────────

function M.status(ctx)
  return {
    installed = go_present(),
    current   = current_proxy() or "",
    healthy   = true,
    note      = "",
  }
end

function M.plan(ctx, opts)
  local proxy    = trim_slash(opts.proxy or "")
  local sumdb    = opts.sumdb or "sum.golang.google.cn"
  local fallback = opts.fallback
  if fallback == nil then fallback = true end
  local path = goenv_path()

  local changes = {}

  if proxy == "" then
    table.insert(changes, {
      kind   = "config",
      file   = path,
      format = "hash",
      syntax = "kv",
      block  = "go",
      entries = {
        { key = "GOPROXY", value = "https://proxy.golang.org,direct" },
      },
    })
    return { changes = changes, warnings = {} }
  end

  local value = proxy
  if fallback and not value:find("direct", 1, true) then
    value = value .. ",direct"
  end

  table.insert(changes, {
    kind    = "config",
    file    = path,
    format  = "hash",
    syntax  = "kv",
    block   = "go",
    entries = {
      { key = "GOPROXY", value = value },
      { key = "GOSUMDB", value = sumdb },
    },
  })
  return { changes = changes, warnings = {} }
end

function M.benchmark(ctx, urls)
  local out = {}
  for _, u in ipairs(urls) do
    local base = trim_slash(u)
    local ms, ok, err
    -- $base/$module/@v/list is the smallest useful response a module proxy
    -- serves; asking for a non-existent module keeps the payload tiny.
    local t0 = os.clock()
    local r, e = km.http.get(base .. "/keep-mirrors-benchmark/@v/list", { timeout = 8 })
    ms = math.floor((os.clock() - t0) * 1000)
    if r then
      ok = r.status < 500
    else
      err = tostring(e)
    end
    table.insert(out, { name = host_of(base), url = u, ms = ms, ok = ok == true, err = err })
  end
  return out
end

return M
