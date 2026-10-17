-- npm plugin for keep-mirrors.
--
-- Contract v1.0. See docs/plugin-contract.md for the full reference.

local M = {}

M.api       = { major = 1, minor = 0 }
M.id        = "npm"
M.version   = "1.0.0"
M.title     = "Node.js / npm"
M.category  = "language"
M.author    = "keep-mirrors"
M.license   = "MIT"
M.homepage  = "https://github.com/morning-start/keep-mirrors/blob/master/internal/plugins/npm.lua"
M.description = "npm registry，写入 .npmrc 并同步 npm_config_registry"
M.platforms = { "windows", "linux", "darwin" }
M.detects   = { "npm", "node" }
M.envs      = { "npm_config_registry" }

M.presets = {
  { id = "npmmirror", label = "npmmirror（淘宝）",
    opts = { registry = "https://registry.npmmirror.com" } },
  { id = "tencent",   label = "腾讯云",
    opts = { registry = "https://mirrors.cloud.tencent.com/npm/" } },
  { id = "huawei",    label = "华为云",
    opts = { registry = "https://repo.huaweicloud.com/repository/npm/" } },
  { id = "official",  label = "官方源",
    opts = { registry = "" } },
}

M.settings = {
  { key = "registry", type = "string", label = "Registry 地址",
    default = "", validate = "url",
    help = "留空则恢复官方源" },
  { key = "disturl",  type = "string", label = "disturl（可选）",
    default = "", validate = "url",
    help = "node-gyp 预编译二进制的下载地址" },
  { key = "strict_ssl", type = "bool", label = "关闭 strict-ssl",
    default = false,
    help = "仅在企业内网自签证书时才需要" },
}

-- ── helpers ────────────────────────────────────────────────────────────

local function npmrc_path()
  return km.home() .. "/.npmrc"
end

local function trim_slash(s)
  return (s:gsub("/+$", ""))
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
  local s = body:find(">>> keep-mirrors:npm >>>", 1, true)
  if not s then return nil end
  local e = body:find("<<< keep-mirrors:npm <<<", s, true)
  if not e then return nil end
  return body:sub(s, e)
end

local function current_registry()
  -- npm_config_registry outranks every .npmrc, including the project one.
  local v = km.env.get("npm_config_registry")
  if v then return v end
  local blk = current_entries(npmrc_path())
  if blk then
    local r = blk:match("registry%s*=%s*([^\n]+)")
    if r then return (r:gsub("^%s*(.-)%s*$", "%1")) end
  end
  return nil
end

local function npm_present()
  for _, c in ipairs(M.detects) do
    if km.fs.which(c) then return true end
  end
  return false
end

-- ── contract functions ─────────────────────────────────────────────────

function M.status(ctx)
  return {
    installed = npm_present(),
    current   = current_registry() or "",
    healthy   = true,
    note      = "",
  }
end

function M.plan(ctx, opts)
  local registry = trim_slash(opts.registry or "")
  local disturl  = trim_slash(opts.disturl or "")
  local no_ssl   = opts.strict_ssl and true or false
  local path = npmrc_path()

  local changes = {}

  if registry == "" then
    table.insert(changes, { kind = "env-unset", name = "npm_config_registry" })
    table.insert(changes, {
      kind   = "config",
      file   = path,
      format = "hash",
      syntax = "kv",
      block  = "npm",
      entries = {
        { key = "registry", value = "" },
      },
    })
    return { changes = changes, warnings = {} }
  end

  local entries = {
    { key = "registry", value = registry },
  }
  if disturl ~= "" then
    table.insert(entries, { key = "disturl", value = disturl })
  end
  if no_ssl then
    table.insert(entries, { key = "strict-ssl", value = "false" })
  end

  table.insert(changes, {
    kind    = "config",
    file    = path,
    format  = "hash",
    syntax  = "kv",
    block   = "npm",
    entries = entries,
  })
  table.insert(changes, {
    kind  = "env",
    name  = "npm_config_registry",
    value = registry,
  })
  return { changes = changes, warnings = {} }
end

function M.benchmark(ctx, urls)
  local out = {}
  for _, u in ipairs(urls) do
    local base = trim_slash(u)
    local ms, ok, err
    -- npm exposes a dedicated ping endpoint; it is the cheapest health check.
    local t0 = os.clock()
    local r, e = km.http.get(base .. "/-/ping", { timeout = 8 })
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