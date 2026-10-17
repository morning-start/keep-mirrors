-- pip plugin for keep-mirrors.
--
-- Contract v1.0. See docs/plugin-contract.md for the full reference.
--
-- This file is the authoritative definition of how pip's mirror is managed.
-- Editing it changes the behaviour of the tool at runtime; no rebuild needed.

local M = {}

M.api       = { major = 1, minor = 0 }
M.id        = "pip"
M.version   = "1.0.0"
M.title     = "Python / pip"
M.category  = "language"
M.author    = "keep-mirrors"
M.license   = "MIT"
M.homepage  = "https://github.com/keep-mirrors/plugins/blob/main/pip.lua"
M.description = "pip 的 index-url 与 trusted-host，写入 pip.conf 并同步 PIP_INDEX_URL"
M.platforms = { "windows", "linux", "darwin" }
M.detects   = { "python", "python3", "pip", "pip3" }
M.envs      = { "PIP_INDEX_URL", "PIP_EXTRA_INDEX_URL" }

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
    local url = blk:match("index%-url%s*=%s*([^\n]+)")
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

-- ── contract functions ─────────────────────────────────────────────────

function M.status(ctx)
  return {
    installed = python_present(),
    current   = current_index() or "",
    healthy   = true,
    note      = "",
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
    -- Restore the official source: drop the variable and blank the key so
    -- that a stale value in another pip.conf cannot win.
    table.insert(changes, {
      kind = "env-unset",
      name = "PIP_INDEX_URL",
    })
    table.insert(changes, {
      kind   = "config",
      file   = path,
      format = "hash",
      syntax = "ini",
      block  = "pip",
      entries = {
        { section = "global", key = "index-url", value = "" },
      },
    })
    if extra ~= "" then
      table.insert(warnings, "extra-index-url 已保留，可手动移除")
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