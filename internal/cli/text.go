package cli

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	pairv1 "github.com/FooFishes/pairwise/gen/pair/v1"
	"github.com/FooFishes/pairwise/internal/check"
)

// 本文件把响应格式化为面向模型的文本: 先给结果, 再给可直接执行的下一步命令.

// text 累积输出文本.
type text struct{ strings.Builder }

// line 按格式写入一行.
func (t *text) line(format string, args ...any) {
	fmt.Fprintf(&t.Builder, format, args...)
	t.WriteByte('\n')
}

// list 在 items 非空时写入标题行与逐条列表.
func (t *text) list(title string, items []string) {
	if len(items) == 0 {
		return
	}
	t.line("%s", title)
	for _, it := range items {
		t.line("- %s", it)
	}
}

// diag 写入一条诊断与修复指引.
func (t *text) diag(d *pairv1.Diagnostic) {
	loc := d.Path
	if d.Line > 0 {
		loc += ":" + strconv.Itoa(int(d.Line))
	}
	sev := "错误"
	if d.Severity == pairv1.Severity_SEVERITY_WARNING {
		sev = "警告"
	}
	t.line("%s: %s [%s] %s", loc, sev, d.Code, d.Message)
	if d.Fix != "" {
		t.line("  修复操作: %s", d.Fix)
	}
}

type count struct {
	name string
	n    int32
}

// counts 把非零计数拼成 "未确认 2, 假设 3" 的形式, 全为零时返回空字符串.
func counts(cs ...count) string {
	var parts []string
	for _, c := range cs {
		if c.n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", c.name, c.n))
		}
	}
	return strings.Join(parts, ", ")
}

// reviewCounts 返回文档中需要开发者审阅的区块与假设计数.
func reviewCounts(d *pairv1.DocSummary) string {
	return counts(
		count{"未确认", d.UnconfirmedCount}, count{"含未审阅改动", d.ChangedCount},
		count{"假设", d.AssumptionCount}, count{"撤回假设", d.RetractedCount},
	)
}

var kindNames = map[pairv1.DocKind]string{
	pairv1.DocKind_DOC_KIND_MODULE:   "模块",
	pairv1.DocKind_DOC_KIND_FLOW:     "流程",
	pairv1.DocKind_DOC_KIND_DECISION: "待确认问题",
}

var docKinds = []pairv1.DocKind{pairv1.DocKind_DOC_KIND_MODULE, pairv1.DocKind_DOC_KIND_FLOW, pairv1.DocKind_DOC_KIND_DECISION}

var sectionStatusNames = map[pairv1.SectionStatus]string{
	pairv1.SectionStatus_SECTION_STATUS_UNCONFIRMED: "未确认",
	pairv1.SectionStatus_SECTION_STATUS_CONFIRMED:   "已确认",
	pairv1.SectionStatus_SECTION_STATUS_CHANGED:     "已确认区块含未审阅改动",
}

func statusText(res *pairv1.StatusResponse) string {
	var t text
	t.line("工作树 %s, 分支 %s", res.Root, res.Branch)
	n := map[pairv1.DocKind]int32{}
	var review, decisions []string
	for _, d := range res.Docs {
		n[d.Kind]++
		if d.Kind == pairv1.DocKind_DOC_KIND_DECISION {
			decisions = append(decisions, d.Id)
		} else if c := reviewCounts(d); c != "" {
			review = append(review, d.Id+": "+c)
		}
	}
	var cs []count
	for _, k := range docKinds {
		cs = append(cs, count{kindNames[k], n[k]})
	}
	docs := counts(cs...)
	if docs == "" {
		docs = "无"
	}
	t.line("文档: %s", docs)
	t.list("需审阅:", review)
	if len(decisions) > 0 {
		t.line("待确认问题: %s", strings.Join(decisions, ", "))
	}
	if c := counts(count{"暂存", res.Staged}, count{"已发送待读取", res.Triggered}, count{"已读取待闭环", res.Drained}); c != "" {
		t.line("反馈: %s", c)
	}
	if c := counts(count{"错误", res.ErrorCount}, count{"警告", res.WarningCount}); c != "" {
		t.line("校验: %s", c)
	}
	if res.UiUrl != "" {
		t.line("界面: 常驻进程已开放工作台, 执行 `pair serve` 获取带一次性访问令牌的地址")
	}

	var next []string
	if len(res.Docs) == 0 {
		next = append(next, "尚无文档: 按 `pair init` 输出的迁移指引用 `pair update <id> --kind module --file <doc.md>` 创建模块")
	}
	if res.Triggered > 0 {
		next = append(next, "执行 `pair drain` 读取已发送的开发者反馈")
	}
	if res.Drained > 0 {
		next = append(next, "执行 `pair stage list` 查看已读取未闭环的反馈, 并用 `pair update <module#sec> --base <hash> --resolves <id>` 闭环")
	}
	if res.ErrorCount > 0 {
		next = append(next, "执行 `pair check` 查看并修复校验错误")
	}
	if len(review) > 0 || len(decisions) > 0 {
		next = append(next, "开发者执行 `pair serve --open` 在工作台审阅区块与待确认问题")
	}
	if res.Staged > 0 {
		next = append(next, "开发者执行 `pair trigger` 发送暂存区反馈")
	}
	if len(next) == 0 {
		next = append(next, "暂无待处理事项")
	}
	t.list("下一步:", next)
	return t.String()
}

func listText(res *pairv1.ListDocsResponse, kind pairv1.DocKind) string {
	var t text
	for _, k := range docKinds {
		if kind != pairv1.DocKind_DOC_KIND_UNSPECIFIED && k != kind {
			continue
		}
		var items []string
		for _, d := range res.Docs {
			if d.Kind != k {
				continue
			}
			s := d.Id
			if d.Title != "" {
				s += " " + d.Title
			}
			s += " (" + d.Path + ")"
			if k != pairv1.DocKind_DOC_KIND_DECISION {
				if c := counts(count{"区块", d.SectionCount}, count{"过渡项", d.WorkaroundCount}); c != "" {
					s += ": " + c
				}
				if c := reviewCounts(d); c != "" {
					s += ", " + c
				}
			}
			items = append(items, s)
		}
		if len(items) > 0 || kind != pairv1.DocKind_DOC_KIND_UNSPECIFIED {
			t.line("%s (%d):", kindNames[k], len(items))
			for _, it := range items {
				t.line("- %s", it)
			}
		}
	}
	switch {
	case len(res.Docs) > 0:
		t.line("下一步: 执行 `pair show <id>` 查看内容与 base 哈希")
	case kind == pairv1.DocKind_DOC_KIND_DECISION:
		t.line("下一步: 暂无待确认问题; 遇到需要开发者决策的设计取舍时用 `pair ask <id> --file <doc.md>` 提交")
	case kind == pairv1.DocKind_DOC_KIND_FLOW:
		t.line("下一步: 用 `pair update <id> --kind flow --file <doc.md>` 创建流程, 执行 `pair list` 查看全部文档")
	default:
		t.line("下一步: 用 `pair update <id> --kind module --file <doc.md>` 创建模块")
	}
	return t.String()
}

func showText(res *pairv1.ShowResponse) string {
	var t text
	t.WriteString(res.Content)
	if !strings.HasSuffix(res.Content, "\n") {
		t.WriteByte('\n')
	}
	t.line("")
	d := res.GetDoc()
	t.line("base: %s (%s 的整文件哈希)", res.Base, d.GetPath())
	decision := d.GetKind() == pairv1.DocKind_DOC_KIND_DECISION
	// 无法解析的文档不能按区块合并, 只能以完整文档覆盖.
	if i := slices.IndexFunc(d.GetDiagnostics(), func(g *pairv1.Diagnostic) bool { return g.Code == "parse-error" }); i >= 0 {
		t.line("该文件无法解析: %s", d.Diagnostics[i].Message)
		if decision {
			t.line("下一步: 修正问题文档后执行 `pair ask %s --file <doc.md>` 重新提交, 覆盖该文件", d.GetId())
		} else {
			t.line("下一步: 以完整文档 (含 Frontmatter) 覆盖该文件: `pair update %s --base %s --file <doc.md>`", d.GetId(), res.Base)
		}
		return t.String()
	}
	if decision {
		if dec := d.GetDecision(); len(dec.GetTargets()) > 0 {
			t.line("目标进度: %s", progressText(dec.Targets, dec.Progress))
		}
		t.line("下一步: 修改该问题后执行 `pair ask %s --file <doc.md>` 重新提交", d.GetId())
		return t.String()
	}
	if res.SectionId == "" {
		t.line("下一步: 修改单个区块执行 `pair update %s#<sec> --base %s <<'EOF'`, 输入只含该区块; "+
			"提交完整文档 (含 Frontmatter) 执行 `pair update %[1]s --base %[2]s --file <doc.md>`", d.GetId(), res.Base)
		return t.String()
	}
	if i := slices.IndexFunc(d.GetSections(), func(s *pairv1.Section) bool { return s.Id == res.SectionId }); i >= 0 {
		s := d.GetSections()[i]
		t.line("区块状态: %s", sectionStatusNames[s.Status])
		var items []string
		for _, a := range s.Assumptions {
			items = append(items, "假设 "+a.Id+": "+a.Text)
		}
		for _, a := range s.Retracted {
			items = append(items, "撤回假设 "+a.Id+": "+a.Text)
		}
		t.list("假设:", items)
	}
	t.line("下一步: 修改该区块后执行 `pair update %s#%s --base %s <<'EOF'` 提交; 输入首行为 `## 标题 <!-- #%[2]s -->` 时整体替换区块, "+
		"否则保留原标题只替换正文; 以只含 `assumptions:` 的 Frontmatter 开头可整体设置该区块的假设列表", d.GetId(), res.SectionId, res.Base)
	return t.String()
}

func lookupText(res *pairv1.LookupResponse) string {
	var t text
	var matched, unmatched bool
	for _, r := range res.Results {
		switch {
		case r.Path == ".." || strings.HasPrefix(r.Path, "../"):
			t.line("%s: 不在当前工作树内, 与本仓库的模块文档无关", r.Path)
		case r.Ignored:
			t.line("%s: 已被 config.toml 的 ignore_globs 忽略, 不受 `pair check --diff` 约束", r.Path)
		case len(r.Docs) == 0:
			unmatched = true
			t.line("%s: 未命中任何模块 scope", r.Path)
		default:
			matched = true
			t.line("%s:", r.Path)
			for _, d := range r.Docs {
				s := d.Id
				if d.Title != "" {
					s += " " + d.Title
				}
				t.line("- %s (%s)", s, d.Path)
			}
		}
	}
	if matched {
		t.line("下一步: 执行 `pair show <id>` 查看模块规则; 修改源码涉及规则变更时用 `pair update` 同步文档")
	}
	if unmatched {
		t.line("下一步: 未命中的源码若属于新的子系统, 用 `pair update <id> --kind module --file <doc.md>` 新建模块并在 scope 中覆盖它")
	}
	if !matched && !unmatched {
		t.line("下一步: 这些路径无需同步文档")
	}
	return t.String()
}

// optionsText 返回 "opt-a (标题), opt-b (标题)" 形式的方案列表.
func optionsText(items []*pairv1.BlockItem) string {
	var parts []string
	for _, it := range items {
		s := it.Id
		if it.Title != "" {
			s += " (" + it.Title + ")"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, ", ")
}

func askText(res *pairv1.AskResponse, wait bool) string {
	var t text
	verb := "已提交"
	if res.Replaced {
		verb = "已覆盖"
	}
	t.line("%s待确认问题 %s (%s)", verb, res.Id, res.Path)
	dec := res.GetDecision()
	t.line("受影响目标: %s", strings.Join(dec.GetTargets(), ", "))
	t.line("候选方案: %s; 默认方案 %s", optionsText(dec.GetOptions()), dec.GetDefaultOption())
	if res.UiUrl != "" {
		t.line("工作台: 开发者执行 `pair serve --open` 打开后可查看对比卡片并选择")
	}
	if wait {
		t.line("正在等待开发者确认...")
	} else {
		t.line("下一步: 请先按默认方案 %s 继续执行, 问题已保留在队列中; 开发者确认并发送后执行 `pair drain` 读取结论", dec.GetDefaultOption())
	}
	return t.String()
}

func waitText(res *pairv1.WaitDecisionResponse) string {
	if res.Decided {
		return drainText(&pairv1.DrainResponse{Items: []*pairv1.Feedback{res.Feedback}})
	}
	var t text
	t.line("暂未确认, 请先按 default_option %s 继续执行, 问题已保留在队列中 (已等待 %d 秒)", res.DefaultOption, res.TimeoutSecs)
	t.line("下一步: 开发者确认并发送后执行 `pair drain` 读取结论")
	return t.String()
}

// feedbackTitle 返回反馈的单行摘要, 如 "[id: x] 选择方案 opt-a (标题)".
func feedbackTitle(f *pairv1.Feedback) string {
	var s string
	if f.GetKind() == pairv1.FeedbackKind_FEEDBACK_KIND_DECISION {
		s = fmt.Sprintf("[id: %s] 选择方案 %s", f.GetId(), f.GetPick())
		if f.GetPickTitle() != "" {
			s += " (" + f.GetPickTitle() + ")"
		}
	} else {
		s = fmt.Sprintf("[id: %s] 修改说明 %s", f.GetId(), f.GetTarget())
		if f.GetAssumptionId() != "" {
			s += " 假设 " + f.GetAssumptionId()
		}
	}
	if f.GetSteer() {
		s += " [立即转向]"
	}
	return s
}

// feedback 写入一条反馈及其说明与受影响目标.
func (t *text) feedback(f *pairv1.Feedback) {
	t.line("- %s", feedbackTitle(f))
	if f.GetAssumptionText() != "" {
		t.line("  假设内容: %s", f.GetAssumptionText())
	}
	if f.GetNote() != "" {
		t.line("  说明: %s", f.GetNote())
	}
	if len(f.GetTargets()) > 0 {
		t.line("  受影响目标: %s", strings.Join(f.GetTargets(), ", "))
	}
}

// resolveHint 返回闭环一条反馈的命令.
func resolveHint(f *pairv1.Feedback) string {
	if f.GetKind() == pairv1.FeedbackKind_FEEDBACK_KIND_DECISION {
		return fmt.Sprintf("%s: 对每个受影响目标执行 `pair update <module#sec> --base <hash> --resolves %[1]s` "+
			"(某目标确认无需改动时改用 `pair update <module#sec> --resolves %[1]s --no-change \"<原因>\"`); "+
			"全部目标更新或显式结案后工具将自动删除 `.pair/decisions/%[1]s.md`", f.GetId())
	}
	return fmt.Sprintf("%s: 执行 `pair update %s --base <hash> --resolves %[1]s`", f.GetId(), f.GetTarget())
}

func drainText(res *pairv1.DrainResponse) string {
	if len(res.Items) == 0 && len(res.Unresolved) == 0 {
		return "暂无已发送的开发者反馈\n下一步: 继续当前任务\n"
	}
	var t text
	var hints []string
	if len(res.Items) == 0 {
		t.line("暂无已发送的开发者反馈")
	} else {
		t.line("已读取 %d 条开发者确认反馈:", len(res.Items))
		for _, f := range res.Items {
			t.feedback(f)
			hints = append(hints, resolveHint(f))
		}
	}
	if len(res.Unresolved) > 0 {
		t.line("此前已读取尚未闭环 (%d):", len(res.Unresolved))
		for _, f := range res.Unresolved {
			t.feedback(f)
			if len(f.Progress) > 0 {
				t.line("  目标进度: %s", progressText(f.Targets, f.Progress))
			}
			hints = append(hints, resolveHint(f))
		}
	}
	t.line("")
	t.list("下一步要求: 请按上述结论修改业务代码, 随后逐条闭环 (base 由 `pair show <module#sec>` 获取):", hints)
	return t.String()
}

var progressNames = map[string]string{"updated": "已更新", "no_change": "已核对 · 无需改动"}

// progressText 返回 "discover#list 已更新; pick-shop#list 待更新" 形式的目标进度, 没有进度记录的目标为待更新.
func progressText(targets []string, ps []*pairv1.TargetProgress) string {
	var parts []string
	for _, target := range targets {
		s := target + " 待更新"
		if i := slices.IndexFunc(ps, func(p *pairv1.TargetProgress) bool { return p.Target == target }); i >= 0 {
			s = target + " " + progressNames[ps[i].Status]
			if ps[i].Reason != "" {
				s += " (" + ps[i].Reason + ")"
			}
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "; ")
}

// pendingTargets 返回问题尚未更新或显式结案的目标.
func pendingTargets(f *pairv1.Feedback) []string {
	return slices.DeleteFunc(slices.Clone(f.Targets), func(target string) bool {
		return slices.ContainsFunc(f.Progress, func(p *pairv1.TargetProgress) bool { return p.Target == target })
	})
}

func updateText(req *pairv1.UpdateRequest, res *pairv1.UpdateResponse) string {
	var t text
	switch {
	case req.NoChange != "":
		t.line("已将 %s 标记为已核对 · 无需改动: %s", req.Target, req.NoChange)
	case res.Created:
		t.line("已新建 %s (%s)", res.DocId, res.Path)
	case res.Changed:
		t.line("已更新 %s (%s)", req.Target, res.Path)
	default:
		t.line("%s 内容无变化 (%s)", req.Target, res.Path)
	}
	if res.Base != "" {
		t.line("新 base: %s", res.Base)
	}
	if len(res.Resolved) > 0 {
		t.line("已闭环: %s", strings.Join(res.Resolved, ", "))
	}
	for _, id := range res.DeletedDecisions {
		t.line("问题 %s 的全部目标已完成, 已删除 .pair/decisions/%[1]s.md", id)
	}
	if len(res.RemovedAssumptions) > 0 {
		t.line("已移除假设: %s", strings.Join(res.RemovedAssumptions, ", "))
	}
	var retracted []string
	for _, a := range res.Retracted {
		retracted = append(retracted, a.Id+": "+a.Text)
	}
	t.list("已撤回假设 (未关联 --resolves 删去, 已移入 retracted 待开发者知晓):", retracted)
	var open []string
	for _, f := range res.OpenDecisions {
		if p := pendingTargets(f); len(p) > 0 {
			open = append(open, fmt.Sprintf("[id: %s] 剩余目标: %s", f.Id, strings.Join(p, ", ")))
		}
	}
	t.list("剩余未闭环:", open)
	switch {
	case len(open) > 0:
		t.line("下一步: 对剩余目标执行 `pair update <module#sec> --base <hash> --resolves <id>`, " +
			"无需改动的目标执行 `pair update <module#sec> --resolves <id> --no-change \"<原因>\"`")
	case res.Base != "":
		t.line("下一步: 继续修改该文档时使用 `--base %s`", res.Base)
	default:
		t.line("下一步: 继续当前任务")
	}
	return t.String()
}

func stagedText(f *pairv1.Feedback) string {
	var t text
	t.line("已暂存:")
	t.feedback(f)
	t.line("下一步: 执行 `pair trigger` 发送给 Agent (加 `--steer` 要求立即转向)")
	return t.String()
}

func confirmText(res *pairv1.ConfirmResponse) string {
	var t text
	t.line("已确认 %s: %s (写入 confirmed_hash, 清空假设与撤回假设)", res.DocId, strings.Join(res.Sections, ", "))
	t.line("新 base: %s", res.Base)
	t.line("下一步: 继续修改该文档时使用 `--base %s`; 此后再改动这些区块会显示为「%s」", res.Base,
		sectionStatusNames[pairv1.SectionStatus_SECTION_STATUS_CHANGED])
	return t.String()
}

var feedbackStates = []struct {
	state pairv1.FeedbackState
	name  string
}{
	{pairv1.FeedbackState_FEEDBACK_STATE_STAGED, "暂存"},
	{pairv1.FeedbackState_FEEDBACK_STATE_TRIGGERED, "已发送待读取"},
	{pairv1.FeedbackState_FEEDBACK_STATE_DRAINED, "已读取待闭环"},
}

func feedbackListText(res *pairv1.ListFeedbackResponse) string {
	if len(res.Items) == 0 {
		return "暂存区与反馈队列为空\n下一步: 用 `pair stage decide` 或 `pair stage note` 暂存反馈\n"
	}
	var t text
	var staged, triggered, drained bool
	for _, s := range feedbackStates {
		var items []*pairv1.Feedback
		for _, f := range res.Items {
			if f.State == s.state {
				items = append(items, f)
			}
		}
		if len(items) == 0 {
			continue
		}
		staged = staged || s.state == pairv1.FeedbackState_FEEDBACK_STATE_STAGED
		triggered = triggered || s.state == pairv1.FeedbackState_FEEDBACK_STATE_TRIGGERED
		drained = drained || s.state == pairv1.FeedbackState_FEEDBACK_STATE_DRAINED
		t.line("%s (%d):", s.name, len(items))
		for _, f := range items {
			t.feedback(f)
			if len(f.Progress) > 0 {
				t.line("  目标进度: %s", progressText(f.Targets, f.Progress))
			}
		}
	}
	if staged {
		t.line("下一步: 执行 `pair trigger` 发送暂存反馈, 或用 `pair stage remove <id>` 撤销暂存")
	}
	if triggered {
		t.line("下一步: Agent 执行 `pair drain` 读取已发送的反馈")
	}
	if drained {
		t.line("下一步: 对已读取待闭环的反馈执行 `pair update <module#sec> --base <hash> --resolves <id>` 闭环")
	}
	return t.String()
}

func triggerText(res *pairv1.TriggerResponse) string {
	if len(res.Items) == 0 {
		return "暂存区为空, 没有可发送的反馈\n下一步: 用 `pair stage decide` 或 `pair stage note` 暂存反馈\n"
	}
	var t text
	var titles []string
	for _, f := range res.Items {
		titles = append(titles, feedbackTitle(f))
	}
	t.list(fmt.Sprintf("已发送 %d 条反馈:", len(res.Items)), titles)
	t.line("下一步: Agent 执行 `pair drain` 读取")
	return t.String()
}

// bindText 的首行固定为 "bind_id: <id>", after-tool Hook 从中提取绑定 ID 完成会话关联.
// flag 是提示中的命令需要带上的 --session 参数.
func bindText(res *pairv1.BindResponse, flag string) string {
	var t text
	t.line("bind_id: %s", res.BindId)
	session := "未关联会话"
	if res.SessionId != "" {
		session = "会话 " + res.SessionId
	}
	var docs []string
	for _, d := range res.Docs {
		secs := "全部区块"
		if len(d.Sections) > 0 {
			secs = strings.Join(d.Sections, ", ")
		}
		docs = append(docs, fmt.Sprintf("%s: %s (base: %s)", d.DocId, secs, d.Base))
	}
	t.list(fmt.Sprintf("已绑定 (%s):", session), docs)
	if res.Preserve != "" {
		t.line("保持规格模式: %s; 回合结束时修改了源码而未改文档也直接放行, 发现必须改规则时仍可执行 `pair update` 或 `pair ask`", res.Preserve)
	}
	t.list("其他会话正在修改 (与本次绑定不冲突, 提交时按区块自动合并):", res.Notices)
	if res.SessionId != "" {
		t.line("下一步: 本会话修改上述区块时执行 `pair update <doc#sec>%s <<'EOF'`, 已缓存 base, 可省略 `--base`", flag)
	} else {
		t.line("下一步: 修改上述区块时执行 `pair update <doc#sec> --base <base> <<'EOF'`")
	}
	return t.String()
}

// bindConflictText 说明绑定因与其他会话命中同一区块而被拒绝, flag 同 bindText.
func bindConflictText(req *pairv1.BindRequest, res *pairv1.BindResponse, flag string) string {
	var t text
	t.list("拒绝绑定: 以下区块正被其他会话修改, 且双方都没有声明 --preserve, 未记录本次绑定:", res.Conflicts)
	// 未关联会话时无法区分本会话经 after-tool 关联的绑定与其他会话的绑定.
	if res.SessionId == "" {
		t.line("本次绑定未关联会话: 若列出的短码属于本会话, 带上 `--session <短码>` 重新执行")
	}
	t.line("修复操作: 改为只绑定不重叠的区块后重新执行 `pair bind <doc#sec>...%s`; 只修缺陷、调样式或重构而不改规则时执行 "+
		"`pair bind %s --preserve \"<原因>\"%[1]s`; 确需修改这些区块时等对方会话结束 (退出、/clear 或 24 小时无活动后其绑定自动释放) 再绑定",
		flag, strings.Join(req.Targets, " "))
	return t.String()
}

func skipText(res *pairv1.SkipUpdateResponse) string {
	var t text
	t.line("已登记跳过原因: %s", res.Reason)
	t.line("下一步: 正常提交, prepare-commit-msg 钩子将自动追加 `Pair-Skip: %s` trailer; 未安装钩子时请手动加到提交信息末尾", res.Reason)
	return t.String()
}

// checkText 格式化检查结果, rerun 是原样重新执行本次检查的命令.
func checkText(diags []*pairv1.Diagnostic, diff *check.DiffResult, rerun string) string {
	var t text
	var errs, warns int32
	for _, d := range diags {
		t.diag(d)
		if d.Severity == pairv1.Severity_SEVERITY_WARNING {
			warns++
		} else {
			errs++
		}
	}
	if errs > 0 {
		t.line("检查未通过: %s", counts(count{"错误", errs}, count{"警告", warns}))
		t.line("下一步: 按各条「修复操作」处理后重新执行 `%s`", rerun)
		return t.String()
	}
	if warns > 0 {
		t.line("检查通过, 警告 %d", warns)
	} else {
		t.line("检查通过")
	}
	switch {
	case diff == nil:
	case len(diff.Missing) > 0:
		t.line("联动检查: 已按 %s 放行 (%s)", check.SkipTrailer, strings.Join(diff.SkipReasons, "; "))
	default:
		t.line("联动检查: 改动源码 %d 个, 命中 scope 的模块文档均已更新", len(diff.Changed))
	}
	if warns > 0 {
		t.line("下一步: 按各条「修复操作」处理警告, `pair check --strict` 会把警告视为错误")
	} else {
		t.line("下一步: 无需处理")
	}
	return t.String()
}
