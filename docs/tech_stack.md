# Pairwise 技术选型

> 本文记录 Pairwise 各部分的技术选型及理由，产品形态与架构见 [产品与架构设计](pair_product_design.md)。选型原则是优先采用成熟库并按官方推荐方式搭建，产品核心逻辑（区块切分、三路合并、确认状态计算）自己实现；运行时与依赖跟随最新 stable 并钉死版本。

---

## 1. 核心：Go 单二进制

- **语言与版本**：Go，`go.mod` 通过 `toolchain` 钉死到当前最新 stable。Hook 在每次工具调用时都会拉起一次 `pair`，Go 的进程启动开销在毫秒级；交叉编译产出单个静态文件，分发时不依赖任何运行时。
- **CLI 框架**：`spf13/cobra`。使用率最高，子命令、Shell 补全与帮助生成都很成熟。
- **配置**：`.pair/config.toml` 直接用 `pelletier/go-toml/v2` 解析，环境变量只承载少量覆盖项（如 `PAIR_SESSION_ID`）。配置只有一份文件，不引入 viper 这类通用配置框架。
- **常驻进程生命周期**：任意 CLI 命令发现常驻进程未运行时自动在后台拉起，用 `gofrs/flock` 文件锁保证每个工作树只有一个实例。
- **日志**：标准库 `log/slog`。

## 2. 文档解析与合并

这部分是产品核心逻辑，除基础解析外全部自行实现。

- **Markdown 解析**：`yuin/goldmark`，只用来可靠地识别代码块、标题与 HTML 注释的位置，避免把代码块中的 `##` 或注释误判为区块边界。读写一律基于原文字节区间切分与拼接，不经过「解析成语法树再渲染回 Markdown」的往返，保证 Git Diff 中只出现真正修改的行。
- **Frontmatter**：`go.yaml.in/yaml/v3`，即 YAML 官方组织接手维护的 yaml.v3（原 `gopkg.in/yaml.v3` 仓库已归档）。写回时使用 Node API，保留键的顺序与注释。
- **区块级三路合并**：自行实现。按 `<!-- #id -->` 将文档切成扁平区块，对 base、当前文件与提交内容逐块比较，同一区块两侧都有改动时判为冲突。冲突时 CLI 输出的统一格式差异用 `aymanbagabas/go-udiff` 生成；界面中的字词级差异高亮由前端计算。
- **`scope` 匹配**：`bmatcuk/doublestar/v4`，支持 `**` 跨目录 Glob，全局忽略规则复用同一套匹配。
- **哈希**：标准库 SHA-256，`confirmed_hash` 与 `--base` 均取前 12 位十六进制。

## 3. Git 集成

- **直接调用系统 `git` 命令，不使用 `go-git`**。`go-git` 对多 Worktree、`rev-parse --git-dir`、合并驱动与 Git 钩子等场景支持不完整，在大仓库上也明显更慢；而用户机器上必然已安装 `git`，直接调用能保证行为与开发者日常操作完全一致。
- 涉及的能力包括：定位 `$GIT_DIR`、读取暂存区与分支区间差异（`check --diff`）、沿历史回溯 `confirmed_hash` 对应的原文、注册合并驱动与 `prepare-commit-msg` / `commit-msg` 钩子。

## 4. 本地运行态存储

- **结构化状态**：会话、暂存区、待消费队列与事件日志存入 `$GIT_DIR/pair/` 下的 SQLite，驱动用 `modernc.org/sqlite`。它是纯 Go 实现，不依赖 cgo，不影响交叉编译。
- **基准快照**：按 `bases/<hash>` 直接存为文件，按 7 天或 100 份 LRU 淘汰；内容已在 Git 对象库中的可回退读取。
- **文件监听**：`fsnotify`，监听 `.pair/` 与 `.git/HEAD`。

## 5. 进程间通信与接口契约

- **单一 HTTP 接口、两个入口**：常驻进程用标准库 `net/http` 同时监听工作树私有的 Unix Domain Socket（供 CLI）与 `127.0.0.1` 端口（供界面），两侧共用同一套处理逻辑。全平台统一使用 Unix Domain Socket，Windows 10 1803 起原生支持，不再单独实现 Named Pipe。Socket 路径受系统长度上限约束（macOS 为 104 字节），因此不放在 `$GIT_DIR/pair/` 下，而是放在用户临时目录中，以工作树根路径的哈希命名。
- **推送用 SSE**：推送只有常驻进程到界面与插件这一个方向，界面操作走普通请求，因此不需要 WebSocket。事件按递增序号持久化，断线重连凭 `Last-Event-ID` 补发；`pair watch --jsonl` 消费同一条事件流。
- **接口契约**：ConnectRPC + buf，`.proto` 契约文件放在仓库中，生成 Go 服务端、Go 客户端（CLI）与 TypeScript 客户端（界面）。Connect 协议的服务端流在 HTTP/1.1 上同样可用，与 SSE 推送的部署方式一致。
- **本地端口安全**：`pair serve --open` 生成一次性访问令牌附在打开的地址中，服务端同时校验 `Host` 与 `Origin`，防御 DNS 重绑定与跨站请求。

## 6. 图形界面

- **技术栈**：React + TypeScript + Vite。组件基于 shadcn/ui（Radix + Tailwind），交互范式参照 macOS；动效用 Motion（原 framer-motion）；服务端状态用 TanStack Query 管理，收到 SSE 事件后使对应查询失效并重新获取。
- **文档只在 Go 端解析一次**：常驻进程将区块、预设组件与假设列表以结构化 JSON 下发，前端不重复解析 `pw:` 注释指令，避免两套解析器对同一份文档理解不一致。区块正文用 `react-markdown` + `remark-gfm` 渲染。
- **Mermaid**：官方 `mermaid` 包渲染，缩放与平移用 `panzoom`，节点点击通过 Mermaid 的节点 ID 映射回关联区块。
- **分发**：前端构建产物通过 Go `embed` 打入二进制，`pair serve --open` 一条命令即可打开界面。
- **桌面端与 IDE 面板（后续阶段）**：桌面端优先用 Wails v3 套壳，复用同一套 Go 与 Web 代码；若届时 Wails v3 仍未稳定，改用 Tauri 包裹本地页面。IDE 面板通过 Webview 加载同一本地页面。

## 7. 宿主插件（后续阶段）

- 首先提供 Claude Code 插件，将生命周期 Hook 配置与 `SKILL.md` 打包分发，Hook 本身只调用 `pair hook <event>`。
- 随后提供 OpenCode 插件（TypeScript），同样只封装 CLI 调用与 `pair watch --jsonl` 事件流，不包含独立业务逻辑。

## 8. 工程化

- **构建与发布**：GoReleaser 产出多平台二进制并同步更新 Homebrew tap。
- **CI**：项目初始化即接入 GitHub Actions，运行 `golangci-lint`、前端构建与全部测试。
- **测试**：以 `rogpeppe/go-internal/testscript` 为主，每个用例在临时创建的真实 Git 仓库中执行完整 CLI 流程，覆盖区块合并、合并驱动、Git 钩子与 `check --diff`，不使用 Mock。合并算法等纯逻辑另配表驱动单元测试。
