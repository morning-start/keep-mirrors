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
--
-- Crates are only half the traffic: `rustup toolchain install` pulls the
-- compilers themselves from static.rust-lang.org, which a cargo mirror does
-- nothing about. RUSTUP_DIST_SERVER and RUSTUP_UPDATE_ROOT are therefore part
-- of the same preset (rustup 的官方文档确认这两个变量).

local M = {}

M.api       = { major = 1, minor = 0 }
M.id        = "cargo"
M.version   = "1.2.0"
M.title     = "Rust / cargo"
M.category  = "language"
M.author    = "keep-mirrors"
M.license   = "MIT"
M.homepage  = "https://github.com/morning-start/keep-mirrors/blob/master/internal/plugins/cargo.lua"
M.description = "cargo registry 镜像（三张表协同写入 config.toml），并同步 rustup 工具链镜像"
M.platforms = { "windows", "linux", "darwin" }
M.detects   = { "cargo", "rustc" }
M.envs      = { "CARGO_HTTP_MULTIPLEXING", "CARGO_HTTP_CHECK_REVOKE",
                "RUSTUP_DIST_SERVER", "RUSTUP_UPDATE_ROOT" }

-- The source name used inside config.toml.
local MIRROR = "keep-mirrors"

M.presets = {
  { id = "rsproxy", label = "RsProxy（字节跳动）",
    opts = {
      index       = "sparse+https://rsproxy.cn/index/",
      registry    = "https://rsproxy.cn/crates.io-index",
      dist        = "https://rsproxy.cn",
      update_root = "https://rsproxy.cn/rustup",
    } },
  { id = "ustc", label = "中国科学技术大学",
    opts = {
      index       = "sparse+https://mirrors.ustc.edu.cn/crates.io-index/",
      registry    = "sparse+https://mirrors.ustc.edu.cn/crates.io-index/",
      dist        = "https://mirrors.ustc.edu.cn/rust-static",
      update_root = "https://mirrors.ustc.edu.cn/rust-static/rustup",
    } },
  { id = "tuna", label = "清华大学 TUNA",
    opts = {
      index       = "sparse+https://mirrors.tuna.tsinghua.edu.cn/crates.io-index/",
      registry    = "sparse+https://mirrors.tuna.tsinghua.edu.cn/crates.io-index/",
      dist        = "https://mirrors.tuna.tsinghua.edu.cn/rustup",
      update_root = "https://mirrors.tuna.tsinghua.edu.cn/rustup/rustup",
    } },
  { id = "official", label = "官方源",
    note = "crates.io + static.rust-lang.org",
    opts = { index = "", registry = "", dist = "", update_root = "" } },
}

M.settings = {
  { key = "index", type = "string", label = "Sparse index 地址",
    default = "", validate = "url",
    help = "需带 sparse+ 前缀" },
  { key = "registry", type = "string", label = "registry 地址",
    default = "", validate = "url",
    help = "cargo search / cargo info 使用" },
  { key = "dist", type = "string", label = "rustup 工具链根地址",
    default = "", validate = "url",
    help = "RUSTUP_DIST_SERVER，留空则不改" },
  { key = "update_root", type = "string", label = "rustup 自更新地址",
    default = "", validate = "url",
    help = "RUSTUP_UPDATE_ROOT，留空则不改" },
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
  local note = ""
  -- rustup and cargo fail independently: a preset that only half landed shows
  -- "已配置" while `rustup toolchain install` still crawls from the official
  -- host. Empty-vs-set is the whole test — comparing URLs would flag every
  -- mirror whose rustup path differs from what this file happens to know.
  if km.fs.which("rustup") then
    local dist = km.env.get("RUSTUP_DIST_SERVER") or ""
    if cur and cur ~= "" and dist == "" then
      note = "rustup 仍从官方下载工具链"
    elseif (not cur or cur == "") and dist ~= "" then
      note = "rustup 仍指向镜像"
    end
  end
  return {
    installed = cargo_present(),
    current   = cur and cur:gsub("^sparse%+", "") or "",
    healthy   = true,
    note      = note,
  }
end

function M.plan(ctx, opts)
  local index    = trim(opts.index or "")
  local registry = trim(opts.registry or "")
  local dist     = trim(opts.dist or "")
  local update   = trim(opts.update_root or "")
  local revoke   = opts.http_check_revoke
  if revoke == nil then revoke = true end
  local path = config_path()
  -- Writing rustup's variables where rustup is not installed would leave two
  -- dead variables behind that nothing reads; the toolchain is opted into by
  -- having the tool.
  local have_rustup = km.fs.which("rustup") ~= nil

  local changes = {}
  local warnings = {}

  if index == "" and registry == "" then
    -- Restore the official source: strip the managed block so cargo uses
    -- crates.io directly. `replace-with = ''` is NOT the default — cargo
    -- looks for a source whose name is the empty string and every fetch
    -- fails with "could not find a configured source with the name ``".
    if have_rustup then
      table.insert(changes, { kind = "env-unset", name = "RUSTUP_DIST_SERVER" })
      table.insert(changes, { kind = "env-unset", name = "RUSTUP_UPDATE_ROOT" })
    end
    table.insert(changes, {
      kind   = "config-remove",
      file   = path,
      format = "hash",
      block  = "cargo",
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
  if have_rustup and dist ~= "" then
    table.insert(changes, { kind = "env", name = "RUSTUP_DIST_SERVER", value = dist })
    if update ~= "" then
      table.insert(changes, { kind = "env", name = "RUSTUP_UPDATE_ROOT", value = update })
    end
  end
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
