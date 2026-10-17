-- cargo plugin for keep-mirrors.
--
-- Contract v1.0. See docs/plugin-contract.md for the full reference.
--
-- Cargo needs three coordinated tables for a mirror to work fully:
--
--   [source.crates-io]
--   replace-with = 'mirror'
--   [source.mirror]
--   registry = "sparse+https://..."
--   [registries.mirror]
--   index = "sparse+https://..."
--
-- The first two are what dependency resolution uses; the third is what
-- `cargo search` and `cargo info` need. Writing all three is what makes a
-- mirror fully functional rather than half-working.

local M = {}

M.api       = { major = 1, minor = 0 }
M.id        = "cargo"
M.version   = "1.0.0"
M.title     = "Rust / cargo"
M.category  = "language"
M.author    = "keep-mirrors"
M.license   = "MIT"
M.homepage  = "https://github.com/morning-start/keep-mirrors/blob/master/internal/plugins/cargo.lua"
M.description = "cargo registry 镜像，三张表协同写入 config.toml"
M.platforms = { "windows", "linux", "darwin" }
M.detects   = { "cargo", "rustc" }
M.envs      = { "CARGO_HTTP_MULTIPLEXING", "CARGO_HTTP_CHECK_REVOKE" }

-- The source name used inside config.toml.
local MIRROR = "keep-mirrors"

M.presets = {
  { id = "rsproxy", label = "RsProxy（字节跳动）",
    opts = {
      index    = "sparse+https://rsproxy.cn/index/",
      registry = "https://rsproxy.cn/crates.io-index",
    } },
  { id = "ustc", label = "中国科学技术大学",
    opts = {
      index    = "sparse+https://mirrors.ustc.edu.cn/crates.io-index/",
      registry = "sparse+https://mirrors.ustc.edu.cn/crates.io-index/",
    } },
  { id = "tuna", label = "清华大学 TUNA",
    opts = {
      index    = "sparse+https://mirrors.tuna.tsinghua.edu.cn/crates.io-index/",
      registry = "sparse+https://mirrors.tuna.tsinghua.edu.cn/crates.io-index/",
    } },
  { id = "official", label = "官方源",
    opts = { index = "", registry = "" } },
}

M.settings = {
  { key = "index", type = "string", label = "Sparse index 地址",
    default = "", validate = "url",
    help = "需带 sparse+ 前缀" },
  { key = "registry", type = "string", label = "registry 地址",
    default = "", validate = "url",
    help = "cargo search / cargo info 使用" },
  { key = "http_check_revoke", type = "bool", label = "关闭 check-revoke",
    default = true,
    help = "规避 Windows 上 InitializeSecurityContext 失败" },
}

-- ── helpers ────────────────────────────────────────────────────────────

-- CARGO_HOME is honoured only when it is an existing directory. An inherited
-- value pointing at a machine-specific location would otherwise send our
-- writes somewhere the user did not choose.
local function cargo_home()
  local h = km.env.get("CARGO_HOME")
  if h and h ~= "" and km.fs.exists(h) then return h end
  return km.home() .. "/.cargo"
end

local function config_path()
  return cargo_home() .. "/config.toml"
end

local function trim(s)
  return (s:gsub("^%s*(.-)%s*$", "%1"))
end

local function host_of(url)
  local h = url:gsub("^sparse%+", "")
  h = h:gsub("^https?://", "")
  return (h:gsub("/.*$", ""))
end

-- Sentinels are located with a plain (non-pattern) find, so no Lua pattern
-- escaping is involved: '-' is not magic outside a character class, and
-- writing '%-' would make the search lazy and silently never match.
local function current_entries(path)
  local body = km.fs.read(path)
  if not body then return nil end
  local s = body:find(">>> keep-mirrors:cargo >>>", 1, true)
  if not s then return nil end
  local e = body:find("<<< keep-mirrors:cargo <<<", s, true)
  if not e then return nil end
  return body:sub(s, e)
end

local function current_index()
  local blk = current_entries(config_path())
  if not blk then return nil end
  -- Locate the mirror table, then read its registry line. Matching the block
  -- we render ourselves is safer than a TOML parse and survives a hand edit
  -- that reorders keys.
  local s = blk:find("[source." .. MIRROR .. "]", 1, true)
  if not s then return nil end
  local r = blk:match('registry%s*=%s*"([^"]+)"', s)
  if r then return r end
  return nil
end

local function cargo_present()
  for _, c in ipairs(M.detects) do
    if km.fs.which(c) then return true end
  end
  return false
end

-- ── contract functions ─────────────────────────────────────────────────

function M.status(ctx)
  local cur = current_index()
  return {
    installed = cargo_present(),
    current   = cur and cur:gsub("^sparse%+", "") or "",
    healthy   = true,
    note      = "",
  }
end

function M.plan(ctx, opts)
  local index    = trim(opts.index or "")
  local registry = trim(opts.registry or "")
  local revoke   = opts.http_check_revoke
  if revoke == nil then revoke = true end
  local path = config_path()

  local changes = {}
  local warnings = {}

  if index == "" and registry == "" then
    table.insert(changes, {
      kind   = "config",
      file   = path,
      format = "hash",
      syntax = "toml",
      block  = "cargo",
      entries = {
        -- Empty replace-with restores the default registry.
        { section = "source.crates-io", key = "replace-with", value = "''" },
      },
    })
    return { changes = changes, warnings = warnings }
  end

  if registry == "" then
    registry = index
  end

  local entries = {
    { section = "source.crates-io", key = "replace-with", value = "'" .. MIRROR .. "'" },
    { section = "source." .. MIRROR, key = "registry", value = index },
    { section = "registries." .. MIRROR, key = "index", value = registry },
  }
  if revoke then
    table.insert(entries,
      { section = "http", key = "check-revoke", value = "false" })
  end

  table.insert(changes, {
    kind    = "config",
    file    = path,
    format  = "hash",
    syntax  = "toml",
    block   = "cargo",
    entries = entries,
  })
  return { changes = changes, warnings = warnings }
end

function M.benchmark(ctx, urls)
  local out = {}
  for _, u in ipairs(urls) do
    local base = u:gsub("/+$", "")
    local ms, ok, err
    -- The sparse index serves a config.json describing the index itself.
    local t0 = os.clock()
    local r, e = km.http.get(base .. "/config.json", { timeout = 8 })
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
