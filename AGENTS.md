# AGENTS.md

给在此仓库工作的 AI agent 的指引。**先读本文再动手**，尤其是 §3 的硬约束和
§4 的坑——那些都是真实发生过的 bug。

相关文档：[架构设计](docs/architecture.md)、[插件契约](docs/plugin-contract.md)、
[README](README.md)。

---

## 1. 项目是什么

Go 写的终端镜像源管理器。**所有镜像源能力由 Lua 插件提供**（pip / npm /
cargo / go 就是四个普通 Lua 文件，运行时从磁盘加载），Go 负责 TUI、沙箱、
原子写入、备份撤销。

插件在运行期存在于 `%APPDATA%\keep-mirrors\plugins\{official,user}\`，仓库里的
`internal/plugins/` 是首次运行用的种子快照，两者角色不同，见 §3.3。

分层：`cmd/km` → `internal/tui` + `internal/app` → `internal/engine` →
`internal/core` → `internal/{marker,config,env,fsutil,paths}`。

---

## 2. 常用命令

```bash
go build ./...                                    # 编译全部
go vet ./...                                      # 静态检查
go test ./...                                     # 全部测试
go test ./internal/engine/ -run TestSandbox -v    # 单个测试
go build -o km.exe ./cmd/km                       # 产出可执行

# 手动验证（会在真实 home 里写配置，跑完记得 km undo）
.\km.exe list
.\km.exe plan pip --preset=tuna                   # 只看 diff
.\km.exe apply pip --preset=tuna --yes
.\km.exe undo

# 插件
.\km.exe plugin dir                               # 两个插件目录在哪
.\km.exe plugin list                              # 已加载插件与来源
.\km.exe plugin show pip                          # 元数据全貌

# 改了 internal/plugins/*.lua 只影响新安装；要验证当前机器，
# 直接改 %APPDATA%\keep-mirrors\plugins\official\ 下的文件（不用重编译），
# 或者复制到 user\ 再改（不会被同步覆盖）。
```

Windows 环境下用 `pwsh`，路径用反斜杠。

---

## 3. 硬约束

违反这些会制造难以发现的 bug。

### 3.1 插件永远不直接写文件

插件只调用 `km.plan.*` 返回声明式变更列表。**不要**给插件加任何写原语。
备份、diff、撤销、原子写必须留在 Go 侧（`internal/core`）。

### 3.2 托管块名必须与读回路径一致

`plan()` 写块的 `block` 名，和 `status()` 里 `body:find(">>> keep-mirrors:<id> >>>")`
的 `<id>` 必须逐字符相同。不一致 = 写入成功但状态永远显示未配置。

### 3.3 插件在磁盘上，只有一份

插件**不是**编译进二进制的。运行时只读这两个目录：

```
%APPDATA%\keep-mirrors\plugins\official\   ← 官方收录，同步会整体重写
%APPDATA%\keep-mirrors\plugins\user\       ← 用户自带，km 永不写入
```

`user` 按 id 覆盖 `official`，这是「不修改官方副本也能钉住本地修复」的方式。

`internal/plugins/*.lua` 是**种子**，不是运行时来源：首次运行（`official/`
为空时）释放一次，让离线安装可用。之后它不再参与任何加载流程。

三条由此推出的规则：

- 改行为改磁盘上的文件，不用重新编译。仓库里的 `internal/plugins/*.lua`
  改完只影响**新安装**，不影响已经播种过的机器。
- 不要在 `official/` 里手改插件——同步会覆盖。要改就复制到 `user/`。
- `SeedIfEmpty` 的「目录非空就跳过」是刻意的：它同时保证同步进来的新版本
  不被回滚、用户删掉的插件不被复活。

### 3.4 方案集顺序即默认值顺序

`readPresets` **不能排序**。第一个预设是用户没动选择器时的默认值。曾经因为
按 id 排序，cargo 的默认从 rsproxy 变成了 official。

### 3.5 只写用户级配置

不要碰 `/etc`、系统级注册表、需要提权的位置。这是个用户态工具，任何
"为了准确性需要 sudo" 的需求都应该是拒绝实现，而不是加提权流程。

---

## 4. 真实发生过的坑

按踩坑频率排序。每条都在代码注释里也写了。

### 4.1 `%-` 不是转义，是惰性量词（最高频）

Lua pattern 里 `%-` 表示「0 次或多次前一个字符」。写托管块查找时：

```lua
-- 错：静默永远匹配失败，症状是"写成功但 status() 读不回"
body:find(">>> keep%-mirrors:pip >>>", 1, true)

-- 对
body:find(">>> keep-mirrors:pip >>>", 1, true)
```

`-` 在字符类外不是元字符，不需要转义。四个内置插件一开始全中。

**只要你新增/修改任何 Lua 里的 `body:find`，就跑一次 apply → status 往返测试。**

### 4.2 Lua 局部函数必须先声明后使用

```lua
-- 错：current_proxy 在 trim 声明前引用它
local function current_proxy()
  local r = blk:match(...)
  return trim(r)        -- trim 在这里还是 nil
end
local function trim(s) ... end

-- 对：把 trim 的定义移到上面
```

症状：`status()` 报 `attempt to call a non-function object`。

### 4.3 绑定函数不要自行 push 错误值

gopher-lua 里闭包 return 的 N 必须与实际 push 的数量一致。曾经
`km.fs.read` 在文件不存在时内部 push 了一次 nil，闭包又 push 一次并
`return 2`，值栈错位，插件收到垃圾。

规则：**绑定函数只在成功路径 push；失败路径由闭包统一决定 push 什么、
return 几。**

### 4.4 不要用 PowerShell 管道改含中文的 UTF-8 文件

`Get-Content -Raw | -replace | Set-Content` 会把中文变成乱码，已经有
`main.go`、`plugins/*.lua`、`docs/*.md` 被毁过。改这类文件：

- 优先用 `edit` 工具（保持 UTF-8）
- 必须用 PowerShell 时，走 `[System.IO.File]::ReadAllText` /
  `WriteAllText` 并显式传 `UTF8Encoding`
- **不要**用 `-replace` 处理含非 ASCII 的文件

### 4.5 `filepath.Join` 会吃掉盘符冒号

`filepath.Join(home, "C:\\x")` → `home/C:\x`，导致 home 边界检查失效。
`paths.Expand` 对带 `VolumeName` 的输入按绝对路径处理；`backupRel` 拼备份
路径前先把 `:` 去掉。

### 4.6 Windows 注册表无法在测试里隔离

`HKCU\Environment` 没有 per-process 覆盖。测试不要断言某个具体终值，改为
断言「撤销后恢复到 apply 前的值」。文件系统部分用 `sandbox()`（见
`cmd/km/e2e_test.go`）重定向 HOME。

### 4.7 `Describe()` 会丢掉无变化的 change

`Plan.Describe()` 除了填 `Before`/`After`，还会**移除 `Before == After`
的 change**。这是有意的：否则一个已经生效的方案集仍然会产出非空计划，
UI 会显示「2 处待改」、`apply` 会原样重写一遍文件。

两个后果要记住：

- 不要为了「显示得完整」而在 `Describe` 之后手动补 change，它们会被下一
  次 `Describe` 删掉；
- 判断「这个方案集是否已生效」用 `plan.Empty()`，不要自己比对字段。

### 4.8 测速结果必须经消息回到 UI 线程

`app.RunBenchmark()` 返回 `tea.Cmd`，结果以 `app.BenchDoneMsg` 回到
`Update`，再由 `ApplyBench` 落到 model。**不要**在 goroutine 里直接写
`m.Bench` / `m.BenchRunning`——那与 `View` 并发读同一个 struct，是数据
竞争（本机无 gcc，`-race` 跑不起来，所以只能靠约定）。

---

## 5. 怎么加一个新镜像源

以 conda 为例。官方收录的源三个地方要改，缺一个就不完整：

1. 复制 `internal/plugins/pip.lua` 为 `internal/plugins/conda.lua`
2. 改元数据：`M.id = "conda"`、`M.title`、`M.version`（新插件从 `1.0.0`
   起）、`M.description`、`M.detects`、`M.envs`
3. 改 `config_files()` / `find_config()`：找 conda 的配置文件
   （`~/.condarc`），确认语法（YAML）
4. 改 `M.plan()`：写托管块，`syntax = "yaml"`
5. 确认 `M.status()` 里的托管块查找用的是 `keep-mirrors:conda`
6. `go test ./internal/engine/ -run TestSeed`——`TestSeedPluginsLoad` 会自动
   遍历种子包里的每个文件，新插件无需登记就会被测到
7. `go test ./cmd/km/` 的往返测试会覆盖新插件
8. 补进 [插件契约](docs/plugin-contract.md) 的源清单

**不要**为它加任何 Go 代码。如果需要 Go 侧能力，先想清楚能不能用现有的
`km.fs` / `km.env` / `km.exec` / `km.http` 表达。

### 只给自己用的插件

放进 `%APPDATA%\keep-mirrors\plugins\user\`，不必进这个仓库。`user` 按 id
覆盖 `official`，也不会被同步覆盖。用 `km plugin dir` 查路径。

---

## 6. 测试要求

改完必须全绿：

```bash
go build ./... && go vet ./... && go test ./... && gofmt -l .
```

`gofmt -l .` 有输出就是没格式化，跑 `gofmt -w .`。

各类改动的必跑测试：

| 改动 | 必跑 |
|---|---|
| 任何 `internal/plugins/*.lua` | `go test ./internal/engine/ -run TestSeed` |
| 沙箱 / `km.*` API | `go test ./internal/engine/ -run TestSandbox` |
| `Loader` / 播种 / 目录优先级 | `go test ./internal/engine/ -run 'TestSeed\|TestList'` |
| `internal/core` / `internal/env` | `go test ./cmd/km/ ./internal/core/` |
| `internal/app` 状态机 | `go test ./internal/app/`（用假 provider，不需要 Lua） |
| `internal/tui` 渲染 | `go test ./internal/tui/`（用假 provider，不需要终端） |

`internal/app` 和 `internal/tui` 用各自测试文件里的假 provider，所以它们的
测试既不加载 Lua 也不碰真实 home，跑得很快。改动签名时这两个包的编译失败
就是最直接的回归信号。

新增 capability 到 `km.*` 时，**必须**同时加一条沙箱测试证明它不能被滥用。

---

## 7. 代码风格

- 注释解释**为什么**，不解释是什么。代码自己说做了什么。
- 包注释写清这个包的不变量（invariant），参考 `internal/core/plan.go` 开头。
- 不用 `interface{}` 存配置值；用 `map[string]any` 但取用时立即转具体类型。
- 错误信息带上下文：`fmt.Errorf("write %s: %w", path, err)`。
- 平台差异集中在 `*_windows.go` / `*_posix.go`，provider 逻辑不判断平台。
- 新依赖先确认能 `go mod download`；本项目刻意保持依赖很少。

---

## 8. 提交前自查

- [ ] `go build ./... && go vet ./... && go test ./...` 全绿
- [ ] `gofmt -l .` 无输出
- [ ] 改了种子插件，`TestSeedPluginsLoad` 仍然覆盖到它
- [ ] 新增源在契约文档里登记了
- [ ] 新插件填了 `version` / `author` / `license` / `description`
- [ ] 没有用 PowerShell 管道碰含中文的文件
- [ ] 新代码没有给插件开放写文件能力
- [ ] 手动 `km undo` 清掉了自己制造的测试配置
