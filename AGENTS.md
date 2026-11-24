# Pairwise 开发指引

本文写给在本仓库中开发 Pairwise 的人与 Agent。产品是什么见 [README](README.md)；改动任何行为之前先读 [开发计划与补充约定](docs/plan.md)，它是现行规则的唯一事实源。

## 文档分工

- [docs/pair_product_design.md](docs/pair_product_design.md)：产品形态与架构，描述要做成什么。
- [docs/tech_stack.md](docs/tech_stack.md)：各部分的库与工程选型及理由。
- [docs/plan.md](docs/plan.md)：交付范围、包结构、设计文档未写明之处的补充约定与验收标准。两份设计文档与它冲突时以它为准；实现改变了规则就同步改它。

## 环境

- Go：版本由 `go.mod` 的 `toolchain` 钉死，`go` 命令会自动下载。
- 前端：Node 24 与 `web/package.json` 中 `packageManager` 钉死的 pnpm（建议用 Corepack）。
- 代码生成工具 `buf`、`protoc-gen-go`、`protoc-gen-connect-go` 是 `go.mod` 的 `tool` 依赖，用 `go tool` 调用，无需单独安装。

## 常用命令

- 安装前端依赖：`pnpm -C web install --frozen-lockfile`
- 修改 `proto/` 后重新生成：`go tool buf lint && go tool buf generate`（同时生成 `gen/` 与 `web/src/gen/`，两处都不手改）。
- 构建界面：`pnpm -C web build`（产物写入 `web/dist/`，由 `web/embed.go` 打进二进制）。
- 构建二进制：`go build -o bin/pair ./cmd/pair`
- 测试：`go test ./...`；并发相关改动加跑 `go test -race ./internal/...`。
- 单个端到端用例：`go test ./cmd/pair -run TestScript/<脚本名>`
- Lint：`go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0 run ./...` 与 `pnpm -C web lint`，版本与 CI 一致。
- 校验插件：`claude plugin validate .`

## 代码结构

包结构与依赖方向见 plan.md 第 3 节。几条容易踩错的边界：

- `internal/cli` 不依赖 `internal/core`；除 `init`、`check`、`merge-driver`、`git-hook` 外，命令一律经 Unix Socket 调常驻进程。
- `internal/core` 直接实现生成的 `PairServiceHandler`，业务判断都在这里；`internal/hook` 只做宿主 JSON 的解析与渲染。
- 文档读写基于原文字节区间拼接，不经过语法树往返；任何改动都要保证未改动区块与 Frontmatter 的字节不变。
- 文件监听回调中从不写 `.pair/*.md`。
- SQLite 结构变更追加新的迁移，不修改已有迁移。

## 测试

- 端到端用例在 `testdata/script/*.txtar`，用 testscript 在临时 Git 仓库中跑真实二进制；`cmd/pair/script_test.go` 提供 `grab`、`http`、`poll`、`sleep` 等自定义命令。
- 不用 Mock：需要 Git、文件系统或常驻进程的地方都用真实的。
- 改动 Hook 或插件后，在已登录 Claude Code 的环境中运行 `scripts/accept-claude.sh`，用真实的 `claude -p` 会话验收会话身份、`.pair/` 写保护、绑定提示与回合结束拦截。
- 纯逻辑（合并、区块切分）配表驱动单元测试；用例宁少勿碎，只钉住重要行为。

## 本地调试

- 常驻进程日志：`$GIT_DIR/pair/daemon.log`；运行信息：`$GIT_DIR/pair/daemon.json`。
- 停止当前工作树的常驻进程：`pair serve --stop`。重新编译后 CLI 发现构建指纹变化会自动重启它。
- Socket 放在运行目录下的 `pair-<uid>/`，macOS 路径上限 104 字节；临时目录很深时设置较短的 `PAIR_RUNTIME_DIR`（如 `/tmp/pair`）。
- 观察事件流：`pair watch --jsonl`。
- 在临时仓库里试用时把二进制放到独立路径，避免和其他正在运行的常驻进程共用同一个构建指纹。

## 约定

- 代码简洁，不写兼容层与死代码，优先用成熟库。
- 注释只写做什么与为什么，用中文和半角标点。
- 面向模型的 CLI 输出：成功附带「下一步」，失败附带可直接执行的「修复操作」。
- 界面克制，遵循 macOS 交互范式，不写解释性文案。
- 提交：一次提交只做一件事，信息格式为英文 `type(scope)` 加中文标题，例如 `fix(core): 每次请求同步分支与配置`。
