# 架构设计

本文档说明 keep-mirrors 的分层、数据流和关键决策。阅读顺序建议：
先看「核心决策」，再看「分层」，最后按需查「数据流」和「故障模式」。

---

## 1. 核心决策

### 1.1 只有一份契约，插件在磁盘上

pip / npm / cargo / go 四个源不是 Go 内置的特殊逻辑，而是普通 Lua 文件。
它们和用户写的第三方插件走**完全相同**的加载路径、沙箱和 Provider 接口。

运行时只从两个目录读，`user` 按 id 覆盖 `official`：

```
<appdata>/keep-mirrors/plugins/official/   官方收录，同步会整体重写
<appdata>/keep-mirrors/plugins/user/       用户自带，km 永不写入
```

**插件不编译进二进制**。这一点是后续能力的根：能从插件库同步、能热重载、
能让用户自带插件，全都要求「插件在运行期是磁盘上的普通文件」。一旦把 `.lua`
塞进 `embed.FS`，「插件在哪」就变成编译期常量，上面三件事都得推倒重来。

`internal/plugins/*.lua` 是**种子快照**，角色是「让离线安装可用」，不是
运行时来源：`SeedIfEmpty` 在 `official/` 为空时释放一次，之后不再参与加载。
「目录非空就跳过」是刻意的一刀切，它同时保证同步来的新版本不被回滚、用户
删掉的插件不被复活。

代价：Go 侧没有编译期类型检查。用契约版本号（`api.major`）和加载时校验兜底，
并在 `internal/engine/plugins_test.go` 里对种子里每个插件跑 `status()` 和
`plan()`。注意这些测试读的是**同一个 embed FS**，不是磁盘路径——测试和二进制
共享同一份数据源，否则两边会漂移。

### 1.2 插件只提案，Go 才执行

插件**没有任何写文件的原语**。它在 `M.plan()` 里返回一个声明式的变更列表；
Go 侧负责读取当前状态、生成 diff、备份、原子写入。

这个间接层买到了三件原本要每个 provider 各写一遍的东西：

1. dry-run 和真实 apply 走同一条代码路径，用户看到的 diff 就是将要写入的
   内容，不会骗人；
2. 每次 apply 自动备份，撤销永远可用；
3. TUI 能渲染任意插件的计划而不需要懂它的配置格式。

### 1.3 托管块，而不是解析再序列化

配置文件写入采用「托管块」：

```ini
# >>> keep-mirrors:pip >>>
[global]
index-url = https://pypi.tuna.tsinghua.edu.cn/simple
# <<< keep-mirrors:pip <<<
```

**为什么不 parse → 修改 → serialize**：`.npmrc`、`pip.conf` 还好，但
`.gitconfig`、`.cargo/config.toml`、`.ssh/config` 一旦序列化就丢注释、丢
键顺序、丢引号风格。镜像管理器没有资格重排用户的 dotfile。

托管块的语义：
- 块外内容**一字不动**；
- 第二次写入 = 整块替换，幂等；
- 撤销 = 整块摘除，并收敛因此产生的空行。

只有在文件里本来就没有托管块概念、且用户明确要求直接改结构时，才走
section-aware 的结构化改写（且必定先备份）。

---

## 2. 分层

```
┌──────────────────────────────────────────────────────────────┐
│  cmd/km            入口：TUI（默认）/ 无头子命令                │
├──────────────────────────────────────────────────────────────┤
│  internal/app      状态机：provider 列表、方案集、计划、撤销     │
│  internal/tui      bubbletea 渲染（纯函数，不碰文件系统）        │
├──────────────────────────────────────────────────────────────┤
│  internal/engine   Lua 沙箱 + km.* API 绑定 + 插件加载          │
│  internal/registry 已加载插件的集合                             │
├──────────────────────────────────────────────────────────────┤
│  internal/core     Plan / Diff / Apply / Backup / Undo          │
├──────────────────────────────────────────────────────────────┤
│  internal/marker   托管块编辑器（保注释、幂等）                  │
│  internal/config   INI/TOML/KV/YAML 的最小渲染（读回在插件里）   │
│  internal/env      环境变量：Windows HKCU / POSIX profile        │
│  internal/fsutil   原子写（tmp → fsync → rename）、模式保持       │
│  internal/paths    ~ 解析、应用数据目录、插件目录、home 边界检查   │
└──────────────────────────────────────────────────────────────┘
   <appdata>/keep-mirrors/plugins/{official,user}/*.lua
                          ← 所有镜像源能力在这里（运行时来源）
   internal/plugins/*.lua ← 种子快照，仅首次运行释放到 official/
```

依赖方向严格单向：`tui` → `app` → `provider`/`engine` → `core` → 底层原语。
`core` 不认识 Lua，`engine` 不认识 TUI。这条线是让无头模式和 TUI 能共用
全部逻辑的原因。

---

## 3. 包职责

| 包 | 职责 | 关键不变量 |
|---|---|---|
| `paths` | 解析 `~`、应用数据目录、home 边界检查 | `Expand` 必须让 `C:\x` 保持绝对路径，不能拼成 `home/C:\x` |
| `fsutil` | 原子写、读、模式保持、目录创建 | 临时文件与目标同目录，保证 rename 是同一文件系统内的原子操作 |
| `marker` | 托管块的查找 / 替换 / 摘除 | 块外内容字节级不变；半写的块（有头无尾）按不存在处理 |
| `config` | 四种语法的渲染 | 已带引号的值不重复引号；读回不在 Go 侧——插件 `status()` 用 Lua 模式读自己的块 |
| `env` | 环境变量读写与广播 | Windows 写 `HKCU\Environment` + `WM_SETTINGCHANGE`；POSIX 写 profile 托管块 |
| `core` | Plan、Diff、Apply、Backup、Undo | Apply 前必 `Describe`；备份先于任何修改 |
| `provider` | Provider 接口、`Preset` / `Setting` / `Status` 数据类型 | 接口字段与 Lua 契约 1:1；预设顺序即默认值顺序，不排序 |
| `engine` | gopher-lua 沙箱、`km.*` 绑定、加载器 | 每个插件独立 `LState`；调用串行化；带 deadline |
| `registry` | 已加载插件的集合 | 同名 id 后者胜出（user 覆盖 official） |
| `app` | 状态机 | TUI 与 CLI 共用；所有文件访问都经由 provider |
| `tui` | 渲染与按键 | 纯函数，不碰文件系统；每个动作以规范键（方向键 / Enter / Esc）为主，vim 字母键只作别名 |

---

## 4. 关键数据流

### 4.1 一次 apply

```
用户按 a
  │
  ├─► tui.Update ──► app.Model.Apply()
  │                    │
  │                    ├─► provider.Plan(ctx, opts)     ← Lua 插件生成变更列表
  │                    │
  │                    ├─► core.Plan.Describe()          ← 读当前磁盘状态，填 Before/After
  │                    │
  │                    ├─► core.NewBackup(plan)          ← 拷原文件 + 记环境变量原值
  │                    │
  │                    └─► 逐条执行
  │                         ├─ ChangeConfig / ChangeConfigRemove
  │                         │     → marker.Replace / marker.Remove → fsutil.WriteFile
  │                         └─ ChangeEnv / ChangeEnvUnset
  │                               → env.Write / env.Delete (注册表/profile + 广播)
  │
  └─► tui.View() 重新渲染 diff 和状态栏
```

`Describe` 在 apply 前**再跑一次**。用户在读 diff 和按确认之间，文件可能被
别的编辑器改过；用旧 diff 去写会静默丢掉那次修改。

### 4.2 一次状态查询

```
app.Model.Status()
  └─► provider.Status(ctx)
        └─► engine.State.CallMethod(mod, "status", ctx)
              └─► Lua: M.status(ctx)
                    ├─ km.fs.read(配置路径)     ← 只读 home 内
                    ├─ 在托管块里找当前值
                    └─ km.env.get(...)          ← 读本进程环境
```

注意 `status()` 的读回路径必须和 `plan()` 的写入路径**完全一致**，包括
托管块名的拼法。这是开发中最容易出错的地方，见 §6.1。

### 4.3 插件加载

```
cmd/km main
  └─ engine.SetSeed(internal/plugins.FS)     ← 只用于首次播种
     └─ engine.NewLoader()
          ├─ SeedIfEmpty()：official/ 为空时把种子释放进去，否则跳过
          └─ Load()
               ├─ 列出 official/ 和 user/ 下的 *.lua
               ├─ user 同名覆盖 official（按 id 去重，user 赢）
               ├─ 读源码 → New(Options) → LoadChunk
               │     └─ DoString("__km_module = (function()\n" + src + "\nend)()")
               │           ↑ 包裹一层，让插件的 return M 可被 Go 取到
               ├─ 校验 api.major == 1，否则跳过并报告
               └─ 读元数据 → 构造 LuaProvider → 注册进 registry
```

单个插件加载失败不影响其他插件，失败信息进 `diag` 并由调用方打印到
stderr——一个拼错的插件不该让整个工具起不来。

---

## 5. 安全模型

插件是**运行在我们进程里的不可信代码**，所以能力是显式授予的，不是限制的。

| 层 | 机制 | 代码位置 |
|---|---|---|
| 文件 | 只能读 `~` 以内，先解析符号链接再比对；越过边界抛错 | `api.go: readWithinHome` |
| 标准库 | `SkipOpenLibs`，只开 base/string/table/math + 裁剪版 os | `engine.go: openSafe` |
| 全局函数 | `dofile`/`loadfile`/`load`/`require`/`module`/`newproxy`/`collectgarbage` 直接置 nil | `engine.go: removeUnsafeGlobals` |
| os 危险函数 | `execute`/`exit`/`remove`/`rename`/`setenv`/`tmpname` 置 nil | 同上 |
| 超时 | 每次调用 `context.WithTimeout`，VM 在指令边界检查 `ctx.Done()` | `engine.go: CallMethod` |
| 内存 | `L.SetMx(64)`，超限退出进程并留日志 | `engine.go: New` |
| 进程 | `km.exec.run` 白名单 + timeout + kill 子进程 | `api.go: execT` |
| 网络 | 只有 `km.http.get`，仅 http/https，用自己的 client | `api.go: httpT` |
| 作用域 | 只写用户级配置，永不碰 `/etc` 或系统级注册表 | 全局约定 |

**为什么超时能强杀**：gopher-lua 的 VM 主循环每条指令都检查
`L.ctx.Done()`，到点 `RaiseError`。这意味着死循环插件会被中断而不是卡死
UI。代价是上下文检查有性能损耗，但插件调用频率低，可以忽略。

**为什么 Lua 栈不能被插件碰**：`km.fs.read` 这类绑定函数只在自己该 push
的时候 push。混用「内部 push 一次 + 调用方再 push 一次」会导致值栈错位，
表现为插件收到垃圾值——这个 bug 真实发生过，见 §6.4。

---

## 6. 已知故障模式与对策

### 6.1 `%-` 惰性量词（最隐蔽）

Lua pattern 里 `%-` 不是转义，而是「0 次或多次前一个字符」。所以：

```lua
-- 错：静默永远匹配失败
body:find(">>> keep%-mirrors:pip >>>", 1, true)

-- 对：plain find，'-' 在字符类外不是元字符
body:find(">>> keep-mirrors:pip >>>", 1, true)
```

**症状**：写入成功，但 `status()` 永远返回空，界面一直显示「未配置」。
四个官方插件一开始全中。对策：契约文档里写成显式警告，`plugins_test.go`
对每个插件跑 apply → status 往返。

### 6.2 方案集顺序被无意改变

`readPresets` 曾经按 id 排序，导致 cargo 的第一个预设从 rsproxy 变成
official（o < r），用户没动选择器就拿到官方源。**方案集声明顺序即默认值
顺序**，不能排序。

### 6.3 路径展开吞掉盘符

`filepath.Join(home, "C:\\x")` 会产出 `home/C:\x`（Join 把 `:` 当普通
字符），于是 home 边界检查失效，沙箱逃逸测试直接抓到。对策：
`paths.Expand` 对带 `VolumeName` 的路径单独按绝对路径处理。

### 6.4 Lua 值栈错位

绑定函数在「文件不存在」分支里既 `L.Push(LNil)` 又 `return 2`，而调用方
也 push 了一次，栈多出一个值。对策：绑定函数永不自行 push 错误值，统一
由闭包决定 push 什么、return 几。

### 6.5 备份路径含冒号

`backupRel` 把 `C:\Users\...` 拼成 `abs\C:Users`——`Join` 吃掉冒号，生成
非法目录名。对策：卷名去掉 `:` 再拼。

### 6.6 环境变量在测试里泄漏到真机

Windows 注册表没有 per-process 覆盖机制，测试改的是真实用户环境。对策：
`cmd/km/e2e_test.go` 的 `sandbox()` 用 `t.Setenv` + `paths.Reset()` 把
HOME/APPDATA/CARGO_HOME/GOENV 全指到临时目录；注册表则改为断言「撤销后
恢复到 apply 前的值」而不是断言某个具体终值。

### 6.7 `%s` 在 Lua pattern 里包含换行

`registry%s*=%s*([^\n]+)` 读回配置时，`%s` 会吃掉空值后面的换行符，捕获组
落到下一行——通常是 `# <<<` 哨兵行，于是 `status()` 把哨兵当成了当前配置，
界面显示「已配置：# <」。`+` 换 `*` 治不了这个（`%s` 照样跨行），必须把
等号两侧的 `%s` 换成 `[ \t]`，值用 `[^\n]*` 让空值按空匹配。

**症状**：写入空值后界面显示一段注释。四个官方插件在空值路径上全中——
「apply → status 有值往返」测试覆盖不到它，因为正常 apply 永远写非空值。
对策：`TestStatusReadsEmptyValueAsEmpty` 手工构造空值块直接测读回。

### 6.8 「恢复官方源」不能写空值

空值不是默认值，是无效配置：`registry=` 让 npm 每次安装报
`ERR_INVALID_URL`；`replace-with = ''` 让 cargo 找名字为空的 source，
所有 fetch 失败。两者都在真机上验证过（后者曾把开发机的 cargo 弄坏）。

对策：新增 `config-remove` 变更类型（对应 `marker.Remove`，补上了 §1.3
承诺的「撤销 = 整块摘除」语义），四个插件的 official 预设改为摘除托管块。
配套修复 `marker.Remove` 的两个缺陷：它曾对全文件做 `\n\n\n` → `\n\n`
折叠（违反「块外一字不动」），且不回收 Replace 加的分隔行。

---

## 7. 平台差异

| 能力 | Windows | POSIX |
|---|---|---|
| 持久化环境变量 | `HKCU\Environment` + 广播 `WM_SETTINGCHANGE` | 探测 `$SHELL`，写对应 rc 文件托管块 |
| 用户配置根 | `%APPDATA%` | `$XDG_CONFIG_HOME` 或 `~/.config` |
| profile 文件 | `$PROFILE` 或 `Documents\WindowsPowerShell\*.ps1` | `.bashrc` / `.zshrc` / `config.fish` |

平台分支全部收在 `internal/env`（`env_windows.go` / `env_posix.go`）和
`internal/core/env_*.go` 两个地方，provider 逻辑本身不判断平台——它只读
`ctx.Platform`。

---

## 8. 演进预留

以下位置已经为后续功能留好接口，当前未实现：

| 功能 | 预留位置 | 还差什么 |
|---|---|---|
| 从插件库同步 | 已完成，见 §8.2 | 无 |
| 无头 CI 模式 | `app.Model` 与 `tui` 已解耦 | 已可用：`km apply --preset=x --yes` |
| 方案集一键切换 | `Model.Presets` 已按 provider 存 | 一个跨 provider 的 preset 组概念 |
| 命名快照 | `core.Backup` 结构已可序列化 | 导出/导入格式与 `km snapshot` 子命令 |
| 用户插件覆盖 | 已完成：`user/` 按 id 覆盖 `official/` | 无 |
| 本地导入命令 | 目录扫描已就绪 | `km plugin import <path>`：校验元数据、检测 id 冲突、复制进 `user/` |
| 热重载 | 已完成，见 §8.1 | 无 |

### 8.1 热重载（已完成）

TUI 每秒轮询一次插件目录，发现变化就重新编译并换掉整个 provider 集合。

```
tick (1s)
  └─ tea.Cmd goroutine: Reloader.Poll()
       ├─ 扫描 official/ 与 user/，取每个文件 (mtime, size)
       ├─ 与上次快照比对，相同则什么都不做
       └─ 不同则 Load() 全套 → 打包成 ReloadedMsg
  └─ UI 线程: Model.ApplyReload(msg)
       └─ Registry.Replace(新集合) → 保留用户的预设选择 → 重建 plan
```

几个刻意的选择，每一条都有对应的测试：

**轮询而不是 fsnotify。** 被监听的是几个小文本文件，由人手编辑，1 秒延迟
感知不到。而 OS watcher 要处理事件合并、重命名语义和后台 goroutine 的生命
周期，每个平台各有各的坑。若延迟将来真的碍事，换掉 `Poll` 的内部实现不会
影响这个类型的其余部分。

**换指针而不是原地重载。** `gopher-lua` 的 `LState` 不能安全地在执行中途
替换源码。所以重载产生**新的** `LuaProvider`，旧的那个进 retired 队列。

**旧 provider 延迟一轮再关。** `State.Close` 和 `State.CallMethod` 用同一把
互斥锁。若在重载当轮就 Close，一个正在跑 `plan()` 或 `benchmark()` 的调用
会让 Close 阻塞在锁上——而它跑在 UI 线程，界面会卡到调用超时（10 秒）。所以
retired 集合在**下一次**轮询时才关闭。

**「谁该退休」由 `Reloader` 自己决定。** 它持有 `live`（调用方正在用的）和
`retired`（被替换掉的），`Poll` 内部完成 `live → retired` 轮转，调用方只
负责把新集合装进 UI。启动时用 `Adopt()` 把初始集合交进来。

这条不是洁癖：早先的版本让调用方拿着 `Poll()` 的返回值去 `Retire()`，而那
恰恰是**新**集合——于是下一次（往往是空闲的）轮询把界面正在显示的 provider
全关掉。重载当轮一切正常，一秒后才集体报 `plugin state is closed`，症状
极像插件错误。把生命周期收进类型里，就不会再有人传错这个 slice。

**全坏时不切换。** 编辑出错（编辑器保存了半个文件）不会把插件全下掉：能编译
的照常加载，坏的报在诊断里，调用方继续用上一份好的集合。一个插件的语法错误
不该让整个界面变空。

**mtime + size 而不是哈希。** 每秒读一遍全部插件内容太浪费；两个编辑落在同一
mtime 刻度且大小相同很罕见，真发生了下一次编辑也会补上。

**被遮蔽的 official 文件不参与比对。** user 按 id 覆盖 official，所以改一个
已经生效的 official 副本不会产生可观测效果，不该报成变化。

**用户的预设选择跨重载保留。** 重载改的是插件行为，不是用户的选择；重置选择
等于悄悄撤销一次操作。游标也按 provider id 重新锚定，因为标题变了会让排序
变化，索引会指到别的源上。

### 8.2 插件同步（已完成）

官方插件在 `plugins` 分支上独立演化，不受发版节奏约束；`internal/plugins/`
是打 tag 时的快照，作为种子编进二进制。

```
km plugin sync
  ├─ git ls-remote https://github.com/{repo}.git plugins   → commit SHA
  ├─ 与本地记录的 SHA 比对，相同则退出
  ├─ 按该 SHA 逐个下载 internal/plugins/*.lua（raw URL，SHA 固定）
  ├─ 每个文件编译并调 status()/plan() 校验契约
  ├─ 全部通过才落盘（fsutil 原子写）
  └─ 热重载自动捡起
```

「本地记录的 SHA」有两级：`plugins-sync.json`（上次同步）优先，没有这个文件时
回落到二进制内嵌的 `PROVENANCE`。所以刚装好、从没同步过的机器也能正确回答
「已是最新」，而不是把二进制自带的插件再下一遍。

**为什么不用 GitHub REST API。** `/repos/{o}/{r}/branches` 走匿名配额，
60 次/小时，一次后台同步就可能耗尽——实测配额归零后只剩 403。而
`git ls-remote` 走 git 协议、`raw.githubusercontent.com` 是静态文件，两者都
不计配额、都不需要 token。用户装上就能用。

**为什么固定 SHA 下载。** 「解析分支」和「取文件」之间可能有人推送，用分支名
下载会拿到和刚才比对过的不一致的内容。

**为什么全量校验后才落盘。** 半个有效的插件集比拒绝同步更难诊断：用户会看到
一部分源正常、一部分加载失败，却不知道是哪一步坏了。

**为什么不能静默丢源。** 分支上少了二进制自带的某个 id，几乎总是上游失误。
同步拒绝，而不是让一个源从用户界面里悄悄消失。

**为什么插件不可能领先于代码。** `km.*` 只存在于 Go 源码，插件写一个未实现的
函数只会得到 `attempt to call a nil value`。顺序天然是单向的：先改 Go，再改
插件。校验只是兜住手滑写错 `api.major` 的情况。

**为什么用户不需要先同步。** 种子让首次运行开箱即用，同步是追赶路径而不是
前置条件。这也正是打 tag 时要把插件同步回 master 的原因：否则新装用户拿到
的是发布时的旧插件。

### 8.3 种子也要知道自己的来源（PROVENANCE）

只有「同步过的机器知道版本」不够：刚装好的机器没有 `plugins-sync.json`，
它无法回答「我这份二进制带的是哪一版插件」。后果有两个——首次同步必然把
二进制自带的四个文件重新下一遍；以及报 bug 时说不清插件版本。

所以种子目录里多一个 `PROVENANCE`：

```
internal/plugins/
├── pip.lua  npm.lua  cargo.lua  go.lua
└── PROVENANCE        # sha=... / source=... / branch=...
```

用文件而不是 `-ldflags -X` 注入，是因为 `go install` 和 `go build` 不会传
构建参数，漏传一次就静默变成「未标记」，而这种错误没有任何提示。

三个设计点：

**`//go:embed *.lua PROVENANCE`**，而不是只 embed `*.lua`。遍历种子时必须
继续按 `.lua` 过滤，否则 `PROVENANCE` 会被当成插件喂给 Lua VM。

**解析器宽容 BOM。** PowerShell 5 的 `Set-Content -Encoding utf8` 必然写入
BOM，于是第一行变成 `\ufeffsha=`，key 匹配不上，整个文件看起来是空的——
而这种现象从表面完全看不出来。`scripts/stamp-plugins.ps1` 已经改用
`UTF8Encoding($false)` 不再写 BOM，解析器仍然 `TrimPrefix("\ufeff")` 兜底。

**脚本会拒绝不一致的快照。** `stamp-plugins.ps1` 在写文件前逐个比对本地
`.lua` 与目标 SHA 的 blob 哈希，不一致就退出并提示先
`git checkout plugins -- internal/plugins/`。否则记下来的 SHA 是在说谎：
要么谎称更新（掩盖待同步的更新），要么谎称更旧（逼用户白下一遍）。

---

## 9. 测试策略

| 层次 | 测什么 | 在哪 |
|---|---|---|
| 沙箱 | 危险全局函数必须不存在；`~` 外读取必须被拒；死循环必须被中断；契约版本不匹配必须被拒 | `internal/engine/engine_test.go` |
| 契约 | 种子里每个插件都能 load、能 `status()`、能 `plan()`，且 default preset 计划非空 | `internal/engine/plugins_test.go` |
| 往返 | apply 后 `status()` 必须反映改动（防 §6.1）；official 预设必须摘除块且 status 归零（防 §6.8）；空值块读回不得捕获哨兵（防 §6.7） | `cmd/km/e2e_test.go` |
| 纯 env 插件 | flutter 无配置文件：`status()` 必须读持久层而非进程环境，official 必须卸掉两个变量且不碰 `FLUTTER_GIT_URL`，恢复幂等 | `cmd/km/e2e_test.go` `TestFlutterEnvRoundTrip` |
| 撤销 | apply 改变的值，undo 必须恢复到 apply 前；备份经 manifest 往返后仍带环境变量原值 | `cmd/km/e2e_test.go` |
| 热重载 | 首轮不算变化、未改不重载、改/增/删都能发现、坏插件不拖垮其余、全坏时拒绝切换、遮蔽文件不误报、旧 VM 延迟关闭 | `internal/engine/reload_test.go` |
| 插件同步 | SHA 未变不下载、不兼容契约被拒、坏 Lua 被拒、id 不匹配被拒、缺源被拒、全量校验后才落盘、dry-run 不写盘 | `internal/syncs/sync_test.go` |
| 种子身份 | `PROVENANCE` 必须有可用 SHA；格式必须是 40 位小写 hex；不得被当成插件文件；解析器容忍 BOM / CRLF / 引号 / 注释 | `internal/plugins/embed_test.go` |
| 重载 UI 契约 | 预设选择与游标跨重载保留、无变化时状态栏安静、坏插件被报告 | `internal/app/model_test.go` |
| 轮询循环 | tick 必须重新排定下一次（否则监听一秒后静默停止） | `internal/tui/update_test.go` |

测试全部跑在 `sandbox()` 造的临时 home 里，不碰开发者真实 dotfile。
