# Pairwise

Pairwise 让开发者与 Coding Agent 围绕仓库内的产品文档协作：Agent 通过 `pair` 命令读写 `.pair/` 中的模块与流程文档、记录自行补全的假设、提交待确认的设计取舍；开发者在本地工作台审阅改动、确认区块、为问题选择方案，再把反馈一次性发送给 Agent。

## 安装

- Homebrew：`brew install --cask foofishes/tap/pair`
- 从源码构建（需要 Go 与 pnpm，界面产物会打进二进制）：

  ```bash
  pnpm -C web install && pnpm -C web build && go build -o bin/pair ./cmd/pair
  ```

## 快速开始

```bash
cd your-repo
pair init --git-hooks     # 创建 .pair/、注册合并驱动、安装提交钩子
pair serve --open         # 打开本地工作台
```

初始化输出会给出全仓迁移指引：让 Agent 把源码与旧文档拆分为带 `scope` 的模块文档，遗留矛盾写成待确认问题。

## 接入 Claude Code

在 Claude Code 中安装插件，获得会话关联、`.pair/` 写保护、回合结束前的反馈与文档同步检查：

```text
/plugin marketplace add FooFishes/pairwise
/plugin install pair@pairwise
```

不装插件也能用：Agent 按 `SKILL.md` 的指引调用无状态命令，提交时由 `commit-msg` 钩子兜底检查文档是否同步。

## 文档

- [产品与架构设计](docs/pair_product_design.md)
- [技术选型](docs/tech_stack.md)
- [开发计划与补充约定](docs/plan.md)
- 参与开发见 [AGENTS.md](AGENTS.md)
