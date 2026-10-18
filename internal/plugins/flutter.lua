-- flutter plugin for keep-mirrors.
--
-- Contract v1.0. See docs/plugin-contract.md for the full reference.
--
-- Flutter has no config file: the tool reads three environment variables and
-- nothing else. That makes this plugin pure env work — PUB_HOSTED_URL (pub
-- packages), FLUTTER_STORAGE_BASE_URL (SDK downloads/upgrades) and
-- FLUTTER_GIT_URL (`flutter upgrade` clones the framework from here).
--
-- The three are independent knobs: some mirrors host pub only, some host
-- storage only, some all three. A preset that leaves one of them empty keeps
-- whatever is already persisted instead of guessing.

local M = {}

M.api       = { major = 1, minor = 0 }
M.id        = "flutter"
M.version   = "1.0.0"
M.title     = "Flutter / Dart"
M.category  = "language"
M.author    = "keep-mirrors"
M.license   = "MIT"
M.homepage  = "https://github.com/morning-start/keep-mirrors/blob/master/internal/plugins/flutter.lua"
M.description = "PUB_HOSTED_URL / FLUTTER_STORAGE_BASE_URL / FLUTTER_GIT_URL"
M.platforms = { "windows", "linux", "darwin" }
M.detects   = { "flutter", "dart" }
M.envs      = { "PUB_HOSTED_URL", "FLUTTER_STORAGE_BASE_URL", "FLUTTER_GIT_URL" }

M.presets = {
  { id = "flutter-io.cn", label = "Flutter 社区（flutter-io.cn）",
    opts = {
      storage = "https://storage.flutter-io.cn",
      pub     = "https://pub.flutter-io.cn",
    } },
  { id = "tuna", label = "清华大学 TUNA",
    opts = {
      storage = "https://mirrors.tuna.tsinghua.edu.cn/flutter",
      pub     = "https://mirrors.tuna.tsinghua.edu.cn/dart-pub",
    } },
  { id = "sjtu", label = "上海交大 SJTUG",
    opts = {
      storage = "https://mirror.sjtu.edu.cn",
      pub     = "https://mirror.sjtu.edu.cn/dart-pub",
    } },
  { id = "official", label = "官方源",
    note = "pub.dev + storage.googleapis.com",
    opts = { storage = "", pub = "" } },
}

M.settings = {
  { key = "pub",     type = "string", label = "PUB_HOSTED_URL",
    default = "", validate = "url",
    help = "pub 包索引，flutter pub get 使用" },
  { key = "storage", type = "string", label = "FLUTTER_STORAGE_BASE_URL",
    default = "", validate = "url",
    help = "SDK 与 engine 下载，flutter upgrade 使用" },
  { key = "git",     type = "string", label = "FLUTTER_GIT_URL（可选）",
    default = "", validate = "url",
    help = "flutter upgrade 拉取 framework 的 git 地址；留空沿用当前值" },
}

-- ── helpers ────────────────────────────────────────────────────────────

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

local function flutter_present()
  for _, c in ipairs(M.detects) do
    if km.fs.which(c) then return true end
  end
  return false
end

-- ── contract functions ─────────────────────────────────────────────────

function M.status(ctx)
  local cur = km.env.persisted("PUB_HOSTED_URL")
  if not cur or cur == "" then cur = km.env.get("PUB_HOSTED_URL") end
  return {
    installed = flutter_present(),
    current   = trim(cur or ""),
    healthy   = true,
    note      = "",
  }
end

function M.plan(ctx, opts)
  local pub     = trim_slash(opts.pub or "")
  local storage = trim_slash(opts.storage or "")
  local git     = trim_slash(opts.git or "")

  local changes = {}
  local warnings = {}

  if pub == "" and storage == "" then
    -- Restore the official source: unset the variables so flutter talks to
    -- Google directly. An empty value is NOT the default here either — a
    -- blank PUB_HOSTED_URL makes `flutter pub get` resolve against a bogus
    -- base URL, exactly like npm's blank registry.
    table.insert(changes, { kind = "env-unset", name = "PUB_HOSTED_URL" })
    table.insert(changes, { kind = "env-unset", name = "FLUTTER_STORAGE_BASE_URL" })
    -- FLUTTER_GIT_URL is a delegation knob, not a mirror we manage. If the
    -- user set it (gitee, a fork, a proxy), unsetting it would silently
    -- repoint `flutter upgrade` at github.com.
    if git ~= "" then
      table.insert(warnings, "FLUTTER_GIT_URL 已保留，可手动移除")
    end
    return { changes = changes, warnings = warnings }
  end

  if pub ~= "" then
    table.insert(changes, { kind = "env", name = "PUB_HOSTED_URL", value = pub })
  end
  if storage ~= "" then
    table.insert(changes, {
      kind  = "env",
      name  = "FLUTTER_STORAGE_BASE_URL",
      value = storage,
    })
  end
  if git ~= "" then
    table.insert(changes, {
      kind  = "env",
      name  = "FLUTTER_GIT_URL",
      value = git,
    })
  end

  return { changes = changes, warnings = warnings }
end

function M.benchmark(ctx, urls)
  local out = {}
  for _, u in ipairs(urls) do
    local base = trim_slash(u)
    local ms, ok, err
    -- The mirrors proxy dart-pub's API; the packages listing is the cheapest
    -- response that proves the mirror actually serves pub packages.
    local t0 = os.clock()
    local r, e = km.http.get(base .. "/api/packages", { timeout = 8 })
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
