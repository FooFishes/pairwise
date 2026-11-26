---
name: pair
description: 在带有 .pair/ 目录的仓库中工作时使用. 规定如何通过 pair 命令读写模块与流程文档、记录自行补全的假设、提交待确认的设计取舍, 以及处理开发者经工作台发来的反馈. 修改业务源码、调整产品规则或遇到需要开发者拍板的取舍之前都要先读.
---

# Pairwise 协作指引

仓库的产品规则与模块文档放在 `.pair/` 中, 由 `pair` 命令统一读写. 开发者在本地工作台审阅区块、确认假设、为待确认问题选择方案, 再把反馈发送给你.

## 基本规则

- 不要用编辑工具直接改 `.pair/**`, 一律通过 `pair update` 写入; 命令会做三路合并并维护确认状态.
- 每条命令的输出末尾都有 "下一步" 或 "修复操作", 照做即可, 不要猜参数.
- 只把开发者明确说过的内容写成正式规则; 你自行补全的交互细节与边界条件逐条记为该区块的假设.

## 一个回合的流程

1. 开始前执行 `pair drain` 读取开发者已发送的反馈. 每条反馈都带 ID, 必须在本回合闭环.
2. 修改源码前执行 `pair lookup <path...>` 找到关联模块, 再用 `pair show <module#sec>` 读取区块原文与 base 哈希.
3. 启用了 Hook 时, 改源码前先 `pair bind <module#sec>`; 只修缺陷、调样式或重构而不改规则时用 `pair bind <module> --preserve "<原因>"`.
   - 本轮上下文给出 Pairwise 会话短码时, 每条 `pair` 命令都带上 `--session <短码>`.
   - bind 报冲突说明其他会话正在改同一区块: 改为绑定不重叠的区块, 或确认不改规则后加 `--preserve`, 不要绕过.
   - 已绑定的文档在本会话中 `pair update` 可省略 `--base`.
4. 改完代码后更新文档:

   ```bash
   pair update discover#tabs --base <hash> <<'EOF'
   ---
   assumptions:
     - id: shared-sort
       text: 三个分类共用同一排序状态, 切换分类不重置
   ---
   ## 分类栏与排序 <!-- #tabs -->
   正文...
   EOF
   ```

   - 输入首行是 `## 标题 <!-- #sec -->` 时整体替换区块, 否则保留原标题只替换正文.
   - 开头的 Frontmatter 只能写 `assumptions`, 用来整体设置该区块的假设列表; 省略则假设不变.
   - 连续修改同一文档时使用上一次输出的新 base.
   - 宿主拒绝 heredoc 时, 用编辑工具把输入写到工作树内 `.pair/` 以外的临时文件, 改用 `--file <path>` 传入, 用完删除.
5. 确认本次修改不涉及规格变更时执行 `pair skip-update --reason "<原因>"`.
6. 启用了 Hook 时, 回合结束前若有未读或未闭环的反馈、或改了源码却没更新文档, 结束会被拦截; 按拦截原因中的命令处理即可.

## 闭环反馈

- 批注 (`fb-<n>`): 改完后 `pair update <module#sec> --base <hash> --resolves fb-<n>`; 针对某条假设的批注闭环时工具会移除该假设.
- 待确认问题: 对每个受影响目标执行 `pair update <module#sec> --base <hash> --resolves <id>`; 某个目标按所选方案无需改动时执行 `pair update <module#sec> --resolves <id> --no-change "<原因>"`. 全部目标完成后工具自动删除问题文件.

## 解决 `.pair/` 的合并冲突

`git merge` 或 `git rebase` 在 `.pair/` 文档中留下冲突标记时, 该文档无法按区块处理, 只能整体覆盖:

1. `pair show <id>` 读取带冲突标记的全文与 base.
2. 合并 `<<<<<<<` 与 `>>>>>>>` 之间两侧应保留的内容, 删除标记行, 执行 `pair update <id> --base <hash> <<'EOF'` 传入完整文档 (含 Frontmatter); 确认状态沿用当前文件. 冲突在 `.pair/decisions/` 的问题文档中时改用 `pair ask <id> <<'EOF'` 重新提交.
3. `pair check` 通过后 `git add` 该文件, 再完成合并或继续 rebase. 不要用 `git checkout --ours/--theirs` 丢弃另一侧的改动.

## 提交待确认问题

遇到需要开发者拍板的设计取舍时, 不要在回复末尾集中提问, 而是写成问题文档:

```markdown
---
id: closed-shop-policy
targets:
  - discover#list
  - pick-shop#list
default_option: opt-a
---

# 打烊店铺如何处理

<!-- pw:compare id="options" -->
#### 方案 A: 按场景分流 <!-- #opt-a -->
发现页剔除, 选店页保留并置灰. 写明在不同场景下的具体后果.
#### 方案 B: 全部保留 <!-- #opt-b -->
...
<!-- /pw:compare -->
```

- `targets` 列出全仓所有受影响的 `module#sec` (广度), 各方案写明在不同场景下的后果 (深度).
- 提交: `pair ask <id> <<'EOF'` (或 `--file <path>`); 影响面大的基础问题加 `--wait` 等待开发者确认, 超时后按 `default_option` 继续, 问题保留在队列中.

## 文档语法

- 区块: 只有 `## 标题 <!-- #id -->` 是受管区块, `###` 及以下归属所在区块. 模块 Frontmatter 含 `id` 与 `scope` (源码 Glob 列表), 流程只含 `id`.
- 引用: 正文中用 `@module#sec` 引用其他文档的区块.
- 流程、时序与状态: 标准 `mermaid` 代码块.
- 页面结构: `pw-layout` 代码块, 用缩进表达嵌套.
- 多方案对比: `<!-- pw:compare id="x" -->` 内用 `#### 方案 <!-- #opt-a -->` 分隔, 以 `<!-- /pw:compare -->` 结束.
- 多状态场景: `<!-- pw:scenarios id="s" -->` 内用 `#### 场景 <!-- #night -->` 分隔.
- 外部依赖过渡做法: `<!-- pw:workaround id="w" -->` 包裹 `> **过渡做法（解除条件：…）**：…`.
- 写完执行 `pair check` 修复全部错误.
