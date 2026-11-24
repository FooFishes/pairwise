# Pairwise 开发计划

> 本文是 [产品与架构设计](pair_product_design.md) 与 [技术选型](tech_stack.md) 的落地计划，记录交付范围、分阶段顺序、包结构、设计文档未写明之处的补充约定与验收标准。补充约定一经实现即视为现行规则，后续改动同步回写本文。

---

## 1. 交付范围

- 本轮交付：Go 单二进制 `pair`（全功能 CLI + 工作树常驻进程）、内嵌的本地 Web 工作台、Git 合并驱动与提交钩子、`pair hook` 生命周期子命令、Claude Code 插件（Hook 配置 + `SKILL.md`）、CI 与 GoReleaser 配置。
- 后续阶段（本轮不做）：Desktop 桌面端、IDE 内嵌面板、OpenCode 插件。
- CI 与 GoReleaser / Homebrew tap 只提供配置文件，本地无法验证实际发布。

## 2. 阶段

每个阶段都是最终形态的纵向切片，不做过渡性 MVP；CLI 经 Unix Socket 调常驻进程、SQLite 运行态、proto 契约从第一阶段起就是正式实现。

- **阶段 0 · 脚手架**：`go.mod`（钉死全部依赖与 `tool` 依赖）、proto 契约与生成代码、包骨架与跨包类型。
- **阶段 A · 文档引擎**：区块切分、预设组件识别、归一化与哈希、Frontmatter Node 读写、区块级三路合并、仓库定位与配置、SQLite 运行态与基准快照。
- **阶段 B · CLI 与常驻进程**：`init / serve / status / list / show / lookup / update / ask / stage / trigger / drain / check / merge-driver / git-hook / watch`，事件日志与 SSE。
- **阶段 C · Web 工作台**：文档浏览、区块状态过滤、预设组件交互视图、字词级差异、决策对比卡片、暂存区与 Trigger。
- **阶段 D · 会话与宿主接入**：`bind / skip-update / session state / hook *`，Claude Code 插件，CI、GoReleaser、README、AGENTS.md。

每个阶段的完成标准：`go build ./...`、`go vet ./...` 与全部测试通过，并在临时 Git 仓库中用真实二进制跑通该阶段功能。

## 3. 包结构

```text
cmd/pair/            入口，只调用 internal/cli
proto/pair/v1/       ConnectRPC 契约（唯一的接口事实源）
gen/pair/v1/         buf 生成的 Go 代码（只重新生成，不手改）
internal/doc/        Markdown 解析：Frontmatter、扁平区块、预设组件、引用、归一化与哈希、写回
internal/merge/      文档级三路合并（Frontmatter 按键 + 正文按区块），供 update 与 merge-driver 共用
internal/gitx/       对系统 git 命令的薄封装
internal/repo/       定位工作树与 $GIT_DIR、读取 config.toml、加载 .pair/ 全部文档、scope 匹配
internal/store/      $GIT_DIR/pair/ 运行态：SQLite（暂存、队列、会话、事件、跳过原因）与 bases/ 快照
internal/check/      语法与引用校验、check --diff，可脱离常驻进程运行
internal/core/       业务操作，直接实现生成的 PairServiceHandler 接口
internal/daemon/     常驻进程：文件锁、UDS + 127.0.0.1 监听、访问令牌与 Host/Origin 校验、SSE、文件监听
internal/client/     CLI 侧：发现或拉起常驻进程、Connect 客户端、SSE 订阅
internal/cli/        cobra 命令与面向模型的文本输出
internal/hook/       宿主 Hook 输入输出适配（Claude Code JSON）
web/                 React + Vite 工作台；web/embed.go 通过 embed 打包 web/dist
plugin/claude-code/  Claude Code 插件：hooks.json 与 SKILL.md
testdata/script/     testscript 端到端用例
```

依赖方向：`cli → client → gen`；`daemon → core → {doc, merge, repo, store, check, gitx}`；`check → {doc, repo, gitx}`；`merge → doc`。`cli` 不直接依赖 `core`，只有 `init`、`check`、`merge-driver`、`git-hook commit-msg` 这类必须脱离常驻进程运行的命令直接调用 `check / merge / repo`。

## 4. 补充约定

设计文档未写明、实现时按最直接的读法补齐的规则。

### 4.1 进程与通信

- **哪些命令不经常驻进程**：`init`、`check`、`merge-driver`、`git-hook commit-msg` 在进程内直接执行（CI 与 `git merge` 期间不应拉起后台进程）；其余命令一律经 Unix Socket 调用常驻进程，未运行时自动拉起。
- **常驻进程入口**：隐藏子命令 `pair daemon` 以前台方式运行常驻进程，自动拉起时以脱离终端的子进程执行它；`pair serve` 确保常驻进程运行并输出界面地址，`pair serve --stop` 停止当前工作树的常驻进程。
- **Socket 路径**：`$PAIR_RUNTIME_DIR`（未设置时为系统临时目录）下的 `pair-<工作树根路径 SHA-256 前 16 位>.sock`，权限 `0600`。
- **运行信息**：`$GIT_DIR/pair/daemon.json` 记录 pid、socket 路径、HTTP 端口与构建指纹；CLI 每次建立连接时按其中记录的 socket 连接（没有记录时按上一条推算），因此运行目录环境变量与常驻进程不同也能连上；文件锁被占用却连不上时报错并给出 `kill <pid>`。CLI 发现构建指纹与自身不一致（升级或重新编译）时请求旧进程退出，等它释放文件锁后再拉起，并发的多个 CLI 最终连上同一个新实例。构建指纹取可执行文件的大小与修改时间。工作树未初始化时不拉起常驻进程。
- **事件推送**：RPC 走 Connect，事件流是独立的标准 SSE 端点 `GET /events`，用 `id:` 字段携带递增序号，支持 `Last-Event-ID` 请求头与 `?since=` 参数补发；浏览器直接用 `EventSource`，`pair watch --jsonl` 经 Unix Socket 读同一端点。
- **状态校准与配置**：每个请求开始时按当时的 HEAD 与磁盘内容校准分支与文档索引，紧随 `git checkout` 或外部编辑的命令不依赖文件监听的去抖；文件监听只负责推送事件。`.pair/config.toml` 随索引一起重新读取，内容无效时沿用上一份有效配置。
- **退出码**：成功 0，业务错误与检查未通过 1，用法错误 2；常驻进程判定的参数或输入不合法（`InvalidArgument`）同样为 2。
- **界面访问令牌**：`pair serve` 每次输出的地址带一次性 `?token=`，首个请求用它换取 `HttpOnly; SameSite=Strict` 的会话 Cookie 后令牌作废；`Host` 只接受 `127.0.0.1:<port>` 与 `localhost:<port>`，带 `Origin` 的请求必须同源。Unix Socket 入口不校验令牌，由文件权限隔离。

### 4.2 文档模型

- **文档 ID**：`modules/` 与 `flows/` 中的文档 ID 全局唯一，必须与文件名一致；`decisions/` 自成一个命名空间。
- **前言区**：首个 `##` 之前的内容（如 `# 标题` 与导语）作为不受管的前言区参与合并，但不进入 `sections` 表，也不计算确认状态。
- **区块哈希**：从 `##` 标题行起至区块末尾，按设计文档的归一化规则（统一换行、去行尾空白、去首尾空行、保留缩进）计算 SHA-256 前 12 位；标题文字属于区块内容，改标题也算改动。
- **区块状态**：无 `confirmed_hash` 为「未确认」；`confirmed_hash` 与当前哈希一致为「已确认」；不一致为「含未审阅改动」。`assumptions` 与 `retracted` 是独立于状态的两个标记。
- **确认原文**：`stage confirm` 时把区块归一化原文存入 `$GIT_DIR/pair/confirmed/<hash>`；查不到时沿 `git log -- <path>` 回溯并缓存，仍找不到则显示「确认原文未入库」。
- **整文件基准**：`--base` 是文件原始字节的 SHA-256 前 12 位，每次对外输出时全文存入 `$GIT_DIR/pair/bases/<hash>`；缓存未命中时沿该文件的 Git 历史逐版本计算哈希回退查找。

### 4.3 更新与合并

- **`update <id>`**：输入是完整文档（含 Frontmatter）。**`update <id#sec>`**：输入是该区块，首行为 `## 标题 <!-- #sec -->` 时整体替换区块，否则保留原标题只替换正文（沿用原区块标题后与末尾的空行）；输入可以以只含 `assumptions:` 的 Frontmatter 开头，用来整体设置该区块的假设列表，省略则假设不变，此时正文为空表示保留原正文。输入为空或只有空白时按缺少输入拒绝；区块输入使其余区块的切分发生变化（未闭合的代码块或 HTML 注释吞掉其后的区块、夹带其他 `##` 标题）时拒绝写入。
- **合并方式**：以基准快照为 base、磁盘当前文件为 ours、按输入构造的新文档为 theirs 做三路合并。正文按区块 ID 合并（前言区视为一个区块），Frontmatter 按键合并，`assumptions / retracted` 按条目 ID 合并；某侧是否改动按归一化后的内容判断，但保留行尾两个以上空格的 Markdown 硬换行（只差换行符、空行或行尾零星空白不算改动），两侧都改且只差空白不算冲突；同一区块或同一键两侧都改且结果不同即冲突，冲突时拒绝写入并输出统一格式差异。正文区块顺序与按 id 合并的序列顺序三路合并：只有一侧调整顺序时采用该侧，两侧以不同方式调整时记为冲突（`order`）；YAML 映射的键序不计入语义比较，只调整键序不算改动。未改动区块的字节与其来源版本一致，只在合并后新出现的相邻处按该区块的换行符补空行；Frontmatter 无改动时原样保留字节，有改动时只重新编码值有变化的顶层键，其余顶层键（含其前的注释）沿用 ours 或 theirs 的原文字节，单独编码的部分展开 YAML 别名。
- **受控字段**：`update` 结果中的 `confirmed_hash` 与 `retracted` 一律取自磁盘当前文件；随后对 ours 中存在、结果中消失且未被 `--resolves` 关联的假设，追加到同区块 `retracted`。
- **新建文档**：文件不存在时免传 `--base`，只接受整文档输入，`--kind module|flow` 决定目录（默认 `module`）。
- **成功输出**：写入采用临时文件加重命名；成功后输出新的整文件 base，供连续更新使用。

### 4.4 反馈闭环

- **反馈 ID**：待确认问题沿用 decision ID；区块批注由 SQLite 自增序号生成 `fb-<n>`。
- **生命周期**：`staged`（暂存）→ `triggered`（已发送）→ `drained`（Agent 已读取）→ `resolved`（已闭环）。暂存与队列按分支名隔离，切换分支后原分支的条目挂起保留。
- **`--resolves`**：批注只能由覆盖其目标区块的 `update` 闭环，带假设 ID 的批注闭环时工具移除该假设；问题的每个 `target` 由覆盖它的 `update` 或 `--no-change` 计入完成度，全部完成后删除 `.pair/decisions/<id>.md` 并写入结案记录。尚未被开发者选择并发送的问题不能被闭环；以已闭环问题的 ID 重新 `ask` 是新一轮问题，不继承上一轮的进度。
- **`ask`**：写入或覆盖 `.pair/decisions/<id>.md`，校验 `targets` 均可解析、存在 `pw:compare` 容器且 `default_option` 是其中的选项。`--wait` 在开发者对该问题点击 Trigger 后立即返回所选方案并视为已读取（已被 `drain` 读取时同样直接返回所选方案）；`--json` 时输出一个含 `ask` 与 `wait` 两个字段的对象；超时以退出码 0 返回按默认方案继续的提示。
- **`stage confirm`**：立即写回文件，不进入暂存区；省略 `#sec` 时确认文档的全部区块。界面上对单条撤回假设点「知晓」只清除那一条。

### 4.5 校验

- **`check`**：Frontmatter 字段、ID 与文件名一致、每个 `##` 都带锚点、文件级命名空间唯一、容器成对闭合、容器内子项唯一、`@module#sec` 引用可解析、decision `targets` 是不带 `@` 的 `module#sec` 且指向已有的 `##` 区块、`default_option` 有效；`sections` 中指向不存在区块的元数据报警告。
- **`--strict`**：警告视为错误，并且存在任何 `decisions/*.md` 时报错。
- **`--diff`**：不带参数检查暂存区（`--cached`），带 `<base-ref>` 检查 `<base-ref>...HEAD`；区间模式下扫描区间内全部提交信息中的 `Pair-Skip:` trailer。
- **钩子**：`init --git-hooks` 写入 `prepare-commit-msg` 与 `commit-msg`，已存在的非 Pairwise 钩子不覆盖；`commit-msg` 检查通过后，常驻进程已在运行时清除已消费的跳过原因（不为此拉起常驻进程）。

## 5. 验收标准

各阶段审查按此逐条核对。

- `update` 后未改动区块与 Frontmatter 的字节完全一致，Git Diff 只出现真正修改的行。
- 代码块与 HTML 注释中的 `##` 不会被识别为区块边界。
- `update` 忽略输入中的 `confirmed_hash` 与 `retracted`；未关联 `--resolves` 删去的假设进入 `retracted`；关联 `--resolves` 的假设被精确移除，同区块其余假设保留。
- 已存在文档漏传 `--base`（且无绑定基准）时拒绝写入，并输出设计文档 7.1 节格式的修复指令。
- 已确认区块被修改后保留 `confirmed_hash`，状态为「含未审阅改动」，界面高亮改动字词。
- 文件监听回调中从不写 `.pair/*.md`。
- `ask --wait` 超时退出码为 0；`--resolves` 完成全部 `targets` 后 decision 文件被删除，`--no-change` 同样计入。
- 合并驱动对 Frontmatter 邻行改动与不同区块改动自动合并，同区块冲突时写入冲突标记并以非零退出。
- `check --diff` 在源码命中 `scope` 而模块文档未改且无 `Pair-Skip` trailer 时报错并给出修复命令。
- 同一工作树只有一个常驻进程；断线重连凭 `Last-Event-ID` 补发错过的事件。
- 非本机 `Host`、跨站 `Origin`、无令牌或无 Cookie 的界面请求被拒绝。
- 所有面向模型的成功输出附带下一步提示，失败输出附带可直接执行的修复命令。

## 6. 并行开发约定

- 并行修改只按互不重叠的包目录拆分，不使用 Git Worktree。
- 只有主会话执行 `go get`、`go mod tidy`、`buf generate` 与提交；子任务不改 `go.mod`、`proto/` 与 `gen/`，缺依赖或契约字段时在结果中提出。
- 审查只读，修复串行进行。
