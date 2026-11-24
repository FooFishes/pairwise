// Package check 实现语法与引用校验以及文件级联动兜底检查, 不依赖常驻进程.
package check

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/FooFishes/pairwise/internal/doc"
	"github.com/FooFishes/pairwise/internal/gitx"
	"github.com/FooFishes/pairwise/internal/repo"
)

// SkipTrailer 是声明本次提交无需更新模块文档的 trailer 键.
const SkipTrailer = "Pair-Skip"

// Diagnostic 是带文件路径的校验结果.
type Diagnostic struct {
	doc.Diagnostic
	// Path 是相对工作树根目录的路径.
	Path string
}

// Options 控制校验范围.
type Options struct {
	// Strict 把警告视为错误, 并在存在任何 decisions/*.md 时报错.
	Strict bool
}

// Run 校验索引中的全部文档.
func Run(ix *repo.Index, opts Options) []Diagnostic {
	var out []Diagnostic
	for _, e := range ix.Entries {
		ds := Doc(ix, e)
		if opts.Strict && e.Kind == doc.KindDecision {
			// 位于第 1 行, 放在最前以保持按行号排序.
			ds = append([]doc.Diagnostic{{
				Severity: doc.SeverityError, Line: 1, Code: "pending-decision",
				Message: fmt.Sprintf("问题「%s」尚未闭环, 合并前须处理全部待确认问题", e.ID),
				Fix: fmt.Sprintf("由开发者在工作台选择方案并点击 Trigger (或执行 `pair stage decide %s --pick <opt>` 与 `pair trigger`), "+
					"再由 Agent 执行 `pair drain`, 并对每个 target 执行 `pair update <module#sec> --base <hash> --resolves %[1]s` "+
					"(无需改动时改用 `--no-change \"<原因>\"`), 全部 targets 完成后该文件自动删除", e.ID),
			}}, ds...)
		}
		for _, d := range ds {
			if opts.Strict {
				d.Severity = doc.SeverityError
			}
			out = append(out, Diagnostic{Diagnostic: d, Path: e.Rel})
		}
	}
	return out
}

// Doc 校验单份文档: 解析诊断、Frontmatter 字段、引用与问题 targets 能否在索引中解析, 以及问题的方案约束.
// e 可以是尚未写入索引的文档 (如 ask 提交的问题), 只用到 Kind、ID、Doc 与 Err. 结果按行号排序.
func Doc(ix *repo.Index, e *repo.Entry) []doc.Diagnostic {
	if e.Doc == nil {
		fix := fmt.Sprintf("执行 `pair show %s` 查看当前内容与 base, 再执行 `pair update %[1]s --base <hash> --file <doc.md>` 以完整文档 (含 Frontmatter) 覆盖该文件", e.ID)
		if e.Kind == doc.KindDecision {
			fix = fmt.Sprintf("修正问题文档后执行 `pair ask %s --file <doc.md>` 重新提交, 覆盖该文件", e.ID)
		}
		return []doc.Diagnostic{{Severity: doc.SeverityError, Line: 1, Code: "parse-error", Message: e.Err.Error(), Fix: fix}}
	}
	c := &checker{ix: ix, e: e, d: e.Doc, f: e.Doc.Front, diags: slices.Clone(e.Doc.Diags)}
	c.id()
	switch e.Kind {
	case doc.KindModule:
		c.scope()
	case doc.KindDecision:
		c.decision()
	}
	for _, r := range c.d.Refs {
		if reason, fix := c.resolve(r.Target); reason != "" {
			c.add(doc.SeverityError, r.Line, "unresolved-ref",
				fmt.Sprintf("引用 `@%s` 无法解析: %s", r.Target, reason),
				fix+", 并修正该引用; "+c.resubmit())
		}
	}
	c.sections()
	slices.SortStableFunc(c.diags, func(a, b doc.Diagnostic) int { return cmp.Compare(a.Line, b.Line) })
	return c.diags
}

type checker struct {
	ix    *repo.Index
	e     *repo.Entry
	d     *doc.Document
	f     *doc.Front
	diags []doc.Diagnostic
}

func (c *checker) add(sev doc.Severity, line int, code, msg, fix string) {
	c.diags = append(c.diags, doc.Diagnostic{Severity: sev, Line: line, Code: code, Message: msg, Fix: fix})
}

// resubmit 返回修正后重新写入该文档的命令.
func (c *checker) resubmit() string {
	if c.e.Kind == doc.KindDecision {
		return fmt.Sprintf("修改后执行 `pair ask %s --file <doc.md>` 重新提交", c.e.ID)
	}
	return fmt.Sprintf("执行 `pair show %s` 获取 base 后用 `pair update %[1]s --base <hash>` 提交修正", c.e.ID)
}

// frontLine 返回沿 keys 逐层找到的最深一个 Frontmatter 键所在的行号, 一个都找不到时返回 1.
func (c *checker) frontLine(keys ...string) int {
	line, n := 1, c.f.Node
	for _, k := range keys {
		i := 0
		for i+1 < len(n.Content) && n.Content[i].Value != k {
			i += 2
		}
		if i+1 >= len(n.Content) {
			break
		}
		// Frontmatter 文本从文件第 2 行开始.
		line, n = n.Content[i].Line+1, n.Content[i+1]
	}
	return line
}

// id 校验 Frontmatter id 与文件名一致, 以及模块与流程共享命名空间中没有同名文档.
func (c *checker) id() {
	switch id := c.e.ID; {
	case c.f.ID == "":
		c.add(doc.SeverityError, 1, "missing-id", "Frontmatter 缺少 id",
			fmt.Sprintf("在 Frontmatter 补上 `id: %s`; %s", id, c.resubmit()))
	case c.f.ID != id:
		c.add(doc.SeverityError, c.frontLine("id"), "id-mismatch",
			fmt.Sprintf("Frontmatter id「%s」与文件名「%s.md」不一致", c.f.ID, id),
			fmt.Sprintf("把 Frontmatter 改为 `id: %s`; %s", id, c.resubmit()))
	}
	if c.e.Kind == doc.KindDecision {
		return
	}
	// Index.Doc 优先返回模块, 冲突记在流程一侧.
	if o := c.ix.Doc(c.e.ID); o != nil && o.Kind != c.e.Kind {
		c.add(doc.SeverityError, 1, "duplicate-doc-id",
			fmt.Sprintf("文档 ID「%s」已被 %s 使用, 模块与流程共享同一命名空间", c.e.ID, o.Rel),
			fmt.Sprintf("执行 `pair update <新 id> --kind %s` 以新 ID 重建该文档, 同步全部 `@%s#...` 引用后删除原文件", c.e.Kind, c.e.ID))
	}
}

// scope 校验模块的 scope 非空且每项都是合法的 Glob.
func (c *checker) scope() {
	line := c.frontLine("scope")
	if len(c.f.Scope) == 0 {
		c.add(doc.SeverityError, line, "missing-scope", fmt.Sprintf("模块「%s」缺少 scope", c.e.ID),
			fmt.Sprintf("在 Frontmatter 补上关联源码的 Glob 列表, 如 `scope: [\"src/%s/**\"]`; %s", c.e.ID, c.resubmit()))
	}
	for _, g := range c.f.Scope {
		if !doublestar.ValidatePattern(g) {
			c.add(doc.SeverityError, line, "invalid-scope", fmt.Sprintf("scope 中的 %q 不是合法的 Glob", g),
				"按 doublestar 语法修正, 如 `src/**/*.go`; "+c.resubmit())
		}
	}
}

// decision 校验问题的 targets 可解析, 存在 pw:compare 容器且 default_option 是其中的方案.
func (c *checker) decision() {
	line := c.frontLine("targets")
	if len(c.f.Targets) == 0 {
		c.add(doc.SeverityError, line, "missing-targets", "问题缺少 targets",
			"在 Frontmatter 补上受影响的区块列表, 如 `targets: [\"discover#list\"]`; "+c.resubmit())
	}
	for _, s := range c.f.Targets {
		// 目标须是规范的 doc#sec 并指向 ## 区块: 闭环时按区块比对 update 与 --no-change 的目标.
		t, err := doc.ParseTarget(s)
		if err == nil && (t.Section == "" || t.Item != "" || t.String() != s) {
			err = fmt.Errorf("targets 中的 `%s` 不是 module#sec 形式的区块路径", s)
		}
		if err != nil {
			c.add(doc.SeverityError, line, "invalid-target", err.Error(), "改为不带 @ 的 `module#sec`, 其中 sec 是 ## 区块的 ID; "+c.resubmit())
			continue
		}
		if o := c.ix.Doc(t.Doc); o != nil && o.Doc != nil && o.Doc.Section(t.Section) == nil && o.Doc.Container(t.Section) != nil {
			c.add(doc.SeverityError, line, "invalid-target",
				fmt.Sprintf("targets 中的 `%s` 指向容器而不是 ## 区块", s),
				fmt.Sprintf("改为容器所在区块的路径 (执行 `pair show %s` 查看区块 ID); %s", t.Doc, c.resubmit()))
			continue
		}
		if reason, fix := c.resolve(t); reason != "" {
			c.add(doc.SeverityError, line, "unresolved-target",
				fmt.Sprintf("targets 中的 `%s` 无法解析: %s", s, reason),
				fix+", 并修正该目标; "+c.resubmit())
		}
	}

	var options []string
	compares := 0
	blocks := slices.Clone(c.d.PreambleBlocks)
	for _, s := range c.d.Sections {
		blocks = append(blocks, s.Blocks...)
	}
	for _, b := range blocks {
		if b.Kind != doc.BlockCompare {
			continue
		}
		compares++
		for _, it := range b.Items {
			if it.ID != "" {
				options = append(options, it.ID)
			}
		}
	}
	if compares == 0 {
		c.add(doc.SeverityError, 1, "missing-compare", "问题缺少 pw:compare 方案对比容器",
			"用 `<!-- pw:compare id=\"options\" -->` 与 `<!-- /pw:compare -->` 包裹各候选方案, "+
				"每个方案以 `#### 方案 A <!-- #opt-a -->` 开始; "+c.resubmit())
	}
	hint := "取值为 pw:compare 中某个方案的 ID"
	if len(options) > 0 {
		hint = "可选值: " + strings.Join(options, ", ")
	}
	switch line = c.frontLine("default_option"); {
	case c.f.DefaultOption == "":
		c.add(doc.SeverityError, line, "missing-default-option", "问题缺少 default_option",
			fmt.Sprintf("在 Frontmatter 补上 `default_option: <方案 ID>`, %s; %s", hint, c.resubmit()))
	case compares > 0 && !slices.Contains(options, c.f.DefaultOption):
		c.add(doc.SeverityError, line, "invalid-default-option",
			fmt.Sprintf("default_option「%s」不是 pw:compare 中的方案", c.f.DefaultOption),
			fmt.Sprintf("把 default_option 改为已有方案的 ID, %s; %s", hint, c.resubmit()))
	}
}

// resolve 在模块与流程命名空间中解析定位路径, 无法解析时返回原因与修复指引.
// 指向自身的路径按本文档解析, 使尚未写入索引的新文档也能引用自己的区块.
func (c *checker) resolve(t doc.Target) (reason, fix string) {
	d := c.d
	if t.Doc != c.e.ID || c.e.Kind == doc.KindDecision {
		o := c.ix.Doc(t.Doc)
		if o == nil {
			return fmt.Sprintf("模块或流程「%s」不存在", t.Doc), "执行 `pair list` 查看已有的模块与流程 ID"
		}
		// 目标文件无法解析时已在其自身报错, 这里不重复.
		if o.Doc == nil {
			return "", ""
		}
		d = o.Doc
	}
	show := fmt.Sprintf("执行 `pair show %s` 查看可引用的区块与容器 ID", t.Doc)
	switch {
	case t.Section == "":
	case t.Item == "":
		if d.Section(t.Section) == nil && d.Container(t.Section) == nil {
			return fmt.Sprintf("「%s」中没有区块或顶层容器「%s」", t.Doc, t.Section), show
		}
	case d.Container(t.Section) == nil:
		return fmt.Sprintf("「%s」中没有容器「%s」", t.Doc, t.Section), show
	case d.Item(t.Section, t.Item) == nil:
		return fmt.Sprintf("容器「%s#%s」中没有子项「%s」", t.Doc, t.Section, t.Item), show
	}
	return "", ""
}

// sections 校验 sections 元数据: 指向不存在区块时报警告, 同一区块内假设 id 缺失或重复时报错.
func (c *checker) sections() {
	for _, id := range c.f.SectionOrder {
		line := c.frontLine("sections", id)
		if c.d.Section(id) == nil {
			c.add(doc.SeverityWarning, line, "stale-meta",
				fmt.Sprintf("Frontmatter sections 中的「%s」没有对应的区块", id),
				fmt.Sprintf("锚点被改名或区块被误删时, 恢复锚点为 `<!-- #%s -->` 的区块; %s", id, c.resubmit()))
		}
		m := c.f.Sections[id]
		if m == nil {
			continue
		}
		fix := c.resubmit()
		if c.e.Kind != doc.KindDecision {
			fix = fmt.Sprintf("执行 `pair show %s#%s` 获取 base 后, 用以只含 `assumptions:` 的 Frontmatter 开头的区块输入执行 "+
				"`pair update %[1]s#%[2]s --base <hash>` 整体改写假设列表", c.e.ID, id)
		}
		seen := map[string]bool{}
		for _, a := range m.Assumptions {
			switch {
			case a.ID == "":
				c.add(doc.SeverityError, line, "missing-assumption-id",
					fmt.Sprintf("区块「%s」的假设「%s」缺少 id", id, a.Text), "为该假设补上区块内唯一的短 ID; "+fix)
			case seen[a.ID]:
				c.add(doc.SeverityError, line, "duplicate-assumption",
					fmt.Sprintf("区块「%s」内假设 id「%s」重复", id, a.ID), "为其中一条假设改用区块内唯一的短 ID; "+fix)
			}
			seen[a.ID] = true
		}
	}
}

// DiffOptions 控制 check --diff 的比较范围.
type DiffOptions struct {
	// BaseRef 为空时检查暂存区, 否则检查 BaseRef...HEAD.
	BaseRef string
	// Message 是暂存区模式下待提交的提交信息, 用于读取 Pair-Skip trailer.
	Message string
}

// Missing 是源码有改动但模块文档未更新的模块.
type Missing struct {
	DocID string
	// Path 是模块文档相对路径.
	Path string
	// Files 是命中该模块 scope 的改动源码.
	Files []string
}

// DiffResult 是 check --diff 的结果.
type DiffResult struct {
	// Changed 是排除 .pair/ 与 ignore_globs 后的改动源码.
	Changed []string
	// Missing 按首个命中的改动源码排序.
	Missing     []Missing
	SkipReasons []string
}

// OK 报告检查是否通过: 没有漏更新的模块, 或提交信息带有 Pair-Skip trailer.
func (r *DiffResult) OK() bool {
	return len(r.Missing) == 0 || len(r.SkipReasons) > 0
}

// Diff 找出改动源码命中 scope 但对应模块文档未改动的模块.
// 跳过原因在暂存区模式下取自 Message, 在区间模式下取自区间内全部提交信息.
func Diff(ctx context.Context, r *repo.Repo, ix *repo.Index, opts DiffOptions) (*DiffResult, error) {
	cached := opts.BaseRef == ""
	files, err := r.Git.ChangedFiles(ctx, cached, opts.BaseRef)
	if err != nil {
		return nil, err
	}
	touched := make(map[string]bool, len(files))
	for _, f := range files {
		touched[f] = true
	}
	res := &DiffResult{}
	for _, f := range files {
		if r.Ignored(f) {
			continue
		}
		res.Changed = append(res.Changed, f)
		for _, e := range ix.ModulesFor(f) {
			if touched[e.Rel] {
				continue
			}
			i := slices.IndexFunc(res.Missing, func(m Missing) bool { return m.DocID == e.ID })
			if i < 0 {
				i = len(res.Missing)
				res.Missing = append(res.Missing, Missing{DocID: e.ID, Path: e.Rel})
			}
			res.Missing[i].Files = append(res.Missing[i].Files, f)
		}
	}
	msgs := []string{opts.Message}
	if !cached {
		if msgs, err = r.Git.CommitMessages(ctx, opts.BaseRef); err != nil {
			return nil, err
		}
	}
	for _, m := range msgs {
		res.SkipReasons = append(res.SkipReasons, gitx.Trailers(m, SkipTrailer)...)
	}
	return res, nil
}
