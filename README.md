# keep-mirrors

一个用 Go 写的终端镜像源管理器。**所有镜像源能力都由 Lua 插件提供**，Go 只负责 TUI、安全沙箱、原子写入、备份与撤销。

## 为什么是 Lua 插件

加一种新镜像源（比如 conda、maven、deno）不需要改 Go 代码、不需要重新编译：
在 `plugins/` 下放一个 `.lua` 文件即可。内置的 pip / npm / cargo / go 本身就是普通插件，
改 `plugins/pip.lua` 保存后重启就生效。

插件**永远不直接写文件**。它调用 `km.plan.*` 声明"我想改这个文件的这个键"，
Go 侧负责：先备份 → 给你看 diff → 确认后原子写入。所以一个写错的插件也改不坏你的配置文件。

## 编译与运行

```bash
go build -o km ./cmd/km

./km                 # 打开 TUI
./km list            # 列出所有源和当前状态
./km plan cargo      # 只看 diff，不写入
./km apply cargo --preset=tuna --yes
./km bench cargo     # 测速所有方案集
./km undo            # 撤销上一次应用
./km backups         # 备份列表
./km plugin list     # 已加载的插件
./km plugin show pip # 某个插件的元数据
```

## 安全边界

插件跑在我们进程里，所以能力是显式授予的：

| 机制 | 说明 |
|---|---|
| 文件 | 只能读 `~` 以内的文件，且只读；`..` 越界直接报错 |
| 标准库 | 只开 `base`/`string`/`table`/`math` + 裁剪版 `os`（仅 time/date/getenv） |
| 禁用 | `io.*`、`os.execute`、`os.exit`、`dofile`、`loadfile`、`require`、`debug` |
| 超时 | 每次调用带 deadline，VM 在指令边界检查，死循环会被强杀 |
| 内存 | 单插件 64 MB 硬上限 |
| 进程 | `km.exec.run` 只允许白名单可执行文件，且带超时 |
| 网络 | 只有 `km.http.get`，且仅 http/https |
| 权限 | 只写用户级配置，永不碰 `/etc` 或系统注册表 |

## 目录结构

```
keep-mirrors/
├── cmd/km/            # 入口（TUI + 无头子命令）
├── internal/
│   ├── paths/         # ~ 解析、应用数据目录、home 边界检查
│   ├── fsutil/        # 原子写（tmp → fsync → rename）、模式保持
│   ├── marker/        # 托管块编辑器：# >>> keep-mirrors:pip >>> ... <<< <<<
│   ├── config/        # INI / TOML / KV / YAML 的最小渲染与读回
│   ├── env/           # 环境变量：Windows HKCU + 广播；POSIX profile 块
│   ├── core/          # Plan / Diff / Apply / Backup / Undo
│   ├── provider/      # Provider 接口（与 Lua 契约 1:1 对应）
│   ├── engine/        # gopher-lua 沙箱、km.* API 绑定、插件加载
│   ├── registry/      # 已加载插件的集合
│   ├── app/           # 状态机（TUI 与无头模式共用）
│   └── tui/           # bubbletea 界面
└── plugins/           # Lua 插件（cmd/km/plugins 是构建时的 embed 副本）
```

## 配置写入方式

所有写入都放在**托管块**里，块外内容一字不动：

```ini
# >>> keep-mirrors:pip >>>
[global]
index-url = https://pypi.tuna.tsinghua.edu.cn/simple
trusted-host = pypi.tuna.tsinghua.edu.cn
# <<< keep-mirrors:pip <<<
```

第二次写入 = 整块替换，幂等。撤销时整块摘除。你的注释、手写配置、工具自有格式全部保留。

## 状态与备份

```
~/.config/keep-mirrors/
├── backups/<时间戳>/     # 每次 apply 前自动快照（原文件 + 原环境变量）
│   └── manifest.txt     # 记录改了什么、环境变量原值是什么
└── plugins/             # 用户插件，同名覆盖内置
```

## 插件开发

见 [docs/plugin-contract.md](docs/plugin-contract.md)。最短可用插件：

```lua
local M = {}
M.id, M.title, M.category = "demo", "Demo", "tool"
M.presets = { { id = "a", label = "A", opts = { url = "https://example.com" } } }
M.settings = { { key = "url", type = "string", label = "URL", default = "" } }

function M.status(ctx) return { installed = true, current = "", healthy = true } end
function M.plan(ctx, opts)
  return { changes = {
    { kind = "env", name = "DEMO_URL", value = opts.url or "" },
  } }
end
return M
```

## 当前状态

已完成：Lua 沙箱与契约、pip / npm / cargo / go 四个插件、托管块写入、备份撤销、
环境变量（Windows HKCU + POSIX profile）、测速、TUI、无头 CLI。

计划中：热重载（fsnotify watch plugins/）、更多源（maven / gradle / gem / conda / apt / docker /
pnpm / deno）、方案集一键切换、命名快照导入导出。

## 文档

| 文档 | 内容 |
|---|---|
| [docs/architecture.md](docs/architecture.md) | 分层、数据流、安全模型、已知故障模式、平台差异、演进预留 |
| [docs/plugin-contract.md](docs/plugin-contract.md) | 插件契约 v1：元数据、三个函数、change 类型、`km.*` API、沙箱限制、排错表 |
| [AGENTS.md](AGENTS.md) | 给 AI agent 的工作指引：硬约束、真实踩过的坑、加新源的步骤、提交前自查 |

> **注意**：`plugins/` 和 `cmd/km/plugins/` 是两份内容相同的目录。前者是源，
> 后者是 `go:embed` 编译进二进制的副本（Go 不允许 embed 上级目录）。
> **改了前者必须同步后者**：
>
> ```bash
> Copy-Item plugins\*.lua cmd\km\plugins\ -Force
> ```
