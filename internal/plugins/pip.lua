-- pip plugin for keep-mirrors.
--
-- Contract v1.0. See docs/plugin-contract.md for the full reference.
--
-- This file is the authoritative definition of how pip's mirror is managed.
-- Editing it changes the behaviour of the tool at runtime; no rebuild needed.

local M = {}

M.api       = { major = 1, minor = 0 }
M.id        = "pip"
M.version   = "1.2.0"
M.title     = "Python / pip"
M.category  = "language"
M.author    = "keep-mirrors"
M.license   = "MIT"
M.homepage  = "https://github.com/morning-start/keep-mirrors/blob/master/internal/plugins/pip.lua"
M.description = "pip 的 index-url 与 trusted-host，写入 pip.conf 并同步 PIP_INDEX_URL；同名方案集一并设置 uv 与 pdm 的索引"
M.platforms = { "windows", "linux", "darwin" }
M.detects   = { "python", "python3", "pip", "pip3" }
M.envs      = { "PIP_INDEX_URL", "PIP_EXTRA_INDEX_URL", "UV_DEFAULT_INDEX", "PDM_PYPI_URL" }

M.presets = {
  { id = "tuna",     label = "清华大学 TUNA",
    opts = { index = "https://pypi.tuna.tsinghua.edu.cn/simple" } },
  { id = "aliyun",   label = "阿里云",
    opts = { index = "https://mirrors.aliyun.com/pypi/simple/" } },
  { id = "ustc",     label = "中国科学技术大学",
    opts = { index = "https://pypi.mirrors.ustc.edu.cn/simple" } },
  { id = "tencent",  label = "腾讯云",
    opts = { index = "https://mirrors.cloud.tencent.com/pypi/simple" } },
  { id = "huawei",   label = "华为云",
    opts = { index = "https://repo.huaweicloud.com/repository/pypi/simple" } },
  { id = "official", label = "官方源",
    note = "pypi.org",
    opts = { index = "" } },
}

M.settings = {
  { key = "index",        type = "string", label = "索引地址",
    default = "", validate = "url",
    help = "留空则恢复官方源" },
  { key = "trusted_host", type = "bool",   label = "写入 trusted-host",
    default = true,
    help = "避免部分镜像的 SSL 校验告警" },
  { key = "extra",        type = "string", label = "备用索引（extra-index-url）",
    default = "", validate = "url",
    help = "可选，与主索引并存" },
}

-- ── helpers ────────────────────────────────────────────────────────────

-- Config files pip reads, in precedence order. We write the first one that
-- already exists, so we never create a second config file and leave the
-- original orphaned.
local function config_files()
  return { "pip/pip.conf", ".config/pip/pip.conf" }
end

local function find_config()
  for _, f in ipairs(config_files()) do
    local p = km.home() .. "/" .. f
    if km.fs.exists(p) then
      return p, f
    end
  end
  -- Default to the classic location when nothing exists yet.
  local p = km.home() .. "/pip/pip.conf"
  return p, "pip/pip.conf"
end

local function host_of(url)
  local h = url:gsub("^https?://", "")
  h = h:gsub("/.*$", "")
  return h
end

local function trim_slash(s)
  return (s:gsub("/+$", ""))
end

-- Read the managed block's entries, or nil when there is no block yet.
--
-- The sentinel is located with a plain (non-pattern) find: '-' is not a Lua
-- pattern metacharacter outside a character class, and writing '%-' would
-- make the match lazy so it would silently never match.
local function current_entries(path)
  local body = km.fs.read(path)
  if not body then return nil end
  local s = body:find(">>> keep-mirrors:pip >>>", 1, true)
  if not s then return nil end
  local e = body:find("<<< keep-mirrors:pip <<<", s, true)
  if not e then return nil end
  return body:sub(s, e)
end

local function current_index()
  local path = find_config()
  local blk = current_entries(path)
  if blk then
    -- Anchored to one line and matching zero-or-more: with `+` an empty
    -- value failed to match on its own line and the capture ran on to the
    -- tail sentinel, so status reported "# <<" as the current index.
    local url = blk:match("index%-url[ \t]*=[ \t]*([^\n]*)")
    if url then return (url:gsub("^%s*(.-)%s*$", "%1")) end
  end
  -- The environment variable takes precedence over any config file.
  return km.env.get("PIP_INDEX_URL")
end

local function python_present()
  for _, c in ipairs(M.detects) do
    if km.fs.which(c) then return true end
  end
  return false
end

-- pip's index setting reaches pip only. uv and pdm are separate Python
-- installers with separate variables, so "switch the Python mirror" that
-- touched one of them would leave the rest quietly downloading from pypi.org.
-- All three follow the same preset; the group is what makes that one action.
--
-- A member is written only when its tool is on PATH: a machine that has never
-- run uv should not collect a dead UV_DEFAULT_INDEX that nothing reads and
-- that the settings screen cannot explain. status() applies the same rule, so
-- a half-configured group (installed after the last apply) reports itself.
local group = {
  { tool = "uv",  env = "UV_DEFAULT_INDEX" },
  { tool = "pdm", env = "PDM_PYPI_URL" },
}

-- uv still reads this deprecated alias, but only while UV_DEFAULT_INDEX is
-- unset (checked against uv 0.13). Harmless next to our value; alive again the
-- moment the mirror is removed, which is why the official restore warns about
-- it rather than leaving a surprise behind.
local UV_LEGACY = "UV_INDEX_URL"

local function tool_present(cmd)
  return km.fs.which(cmd) ~= nil
end

local function group_drift(cur)
  local out = {}
  for _, g in ipairs(group) do
    if tool_present(g.tool) and (km.env.get(g.env) or "") ~= cur then
      table.insert(out, g.tool)
    end
  end
  return out
end

-- ── contract functions ─────────────────────────────────────────────────

function M.status(ctx)
  local cur = current_index() or ""
  local drift = group_drift(cur)
  local note = ""
  if #drift > 0 then
    -- The note is the only place the group can admit it is half applied: the
    -- badge speaks for pip alone, and a user who sees "已配置" would otherwise
    -- never learn that uv still talks to pypi.org.
    note = table.concat(drift, "、") .. " 的索引与 pip 不一致"
  end
  return {
    installed = python_present(),
    current   = cur,
    healthy   = true,
    note      = note,
  }
end

function M.plan(ctx, opts)
  local index = trim_slash(opts.index or "")
  local trusted = opts.trusted_host
  if trusted == nil then trusted = true end
  local extra = trim_slash(opts.extra or "")
  local path, _ = find_config()

  local changes = {}
  local warnings = {}

  if index == "" then
    -- Restore the official source: unset the variable and strip the managed
    -- block, which returns pip to its built-in default. Writing an empty
    -- value here would leave `index-url = ` in pip.conf, and a blank value
    -- is not the default — it is a broken config.
    table.insert(changes, {
      kind = "env-unset",
      name = "PIP_INDEX_URL",
    })
    for _, g in ipairs(group) do
      if tool_present(g.tool) then
        table.insert(changes, { kind = "env-unset", name = g.env })
      end
    end
    table.insert(changes, {
      kind   = "config-remove",
      file   = path,
      format = "hash",
      block  = "pip",
    })
    if extra ~= "" then
      table.insert(warnings, "extra-index-url 已保留，可手动移除")
    end
    if tool_present("uv") and (km.env.get(UV_LEGACY) or "") ~= "" then
      table.insert(warnings,
        UV_LEGACY .. " 仍然设置（uv 已废弃该项）；移除镜像后 uv 会回落到它")
    end
    return { changes = changes, warnings = warnings }
  end

  local entries = {
    { section = "global", key = "index-url", value = index },
  }
  if trusted then
    table.insert(entries, {
      section = "global", key = "trusted-host", value = host_of(index) })
  end
  if extra ~= "" then
    table.insert(entries, {
      section = "global", key = "extra-index-url", value = extra })
  end

  table.insert(changes, {
    kind   = "config",
    file   = path,
    format = "hash",
    syntax = "ini",
    block  = "pip",
    entries = entries,
  })
  table.insert(changes, {
    kind  = "env",
    name  = "PIP_INDEX_URL",
    value = index,
  })
  for _, g in ipairs(group) do
    if tool_present(g.tool) then
      table.insert(changes, { kind = "env", name = g.env, value = index })
    end
  end
  return { changes = changes, warnings = warnings }
end

function M.benchmark(ctx, urls)
  local out = {}
  for _, u in ipairs(urls) do
    local base = trim_slash(u)
    local ms, ok, err
    -- pip's simple index is the cheapest endpoint that proves the mirror
    -- serves packages.
    local t0 = os.clock()
    local r, e = km.http.get(base .. "/simple/", { timeout = 8 })
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