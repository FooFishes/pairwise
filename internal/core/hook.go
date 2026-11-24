package core

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"connectrpc.com/connect/v2"

	pairv1 "github.com/FooFishes/pairwise/gen/pair/v1"
	"github.com/FooFishes/pairwise/internal/doc"
	"github.com/FooFishes/pairwise/internal/hook"
	"github.com/FooFishes/pairwise/internal/repo"
	"github.com/FooFishes/pairwise/internal/store"
)

const (
	stateTriggered = pairv1.FeedbackState_FEEDBACK_STATE_TRIGGERED
	stateDrained   = pairv1.FeedbackState_FEEDBACK_STATE_DRAINED
)

// bindIDRe 匹配 pair bind 文本输出的首行, after-tool 据此把漏传 --session 的绑定关联到当前会话.
var bindIDRe = regexp.MustCompile(`(?m)^bind_id: (b-[0-9]+)$`)

// hookCall 是一次 Hook 调用的上下文.
type hookCall struct {
	ix     *repo.Index
	branch string
	in     *hook.Input
	sess   *store.Session
}

// flag 返回提示中 pair 命令需要追加的参数: 宿主没有会话环境文件时命令不带 PAIR_SESSION_ID, 须显式传短码.
func (h *hookCall) flag() string {
	if h.sess.EnvFile {
		return ""
	}
	return " --session " + h.sess.Code
}

// Hook 处理宿主生命周期事件, 返回交给宿主的 stdout JSON 与需要追加到会话环境文件的内容.
func (w *Workspace) Hook(ctx context.Context, req *pairv1.HookRequest) (*pairv1.HookResponse, error) {
	if !slices.Contains(hook.Events, req.Event) {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Sprintf("未知的 Hook 事件 %q.\n修复操作: 改为 %s 之一", req.Event, strings.Join(hook.Events, ", ")))
	}
	in, err := hook.Parse(req.Payload)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
	}
	ix, branch, err := w.sync(ctx)
	if err != nil {
		return nil, err
	}
	sess, err := w.store.UpdateSession(in.SessionID, func(s *store.Session) {
		switch req.Event {
		case hook.SessionStart:
			s.EnvFile = req.EnvFile != ""
		case hook.PromptSubmit:
			s.State, s.Unverified = "running", ""
		case hook.SessionEnd:
			s.State = "idle"
		}
	})
	if err != nil {
		return nil, err
	}
	h := &hookCall{ix: ix, branch: branch, in: in, sess: sess}
	resp := &pairv1.HookResponse{}
	var out hook.Output
	switch req.Event {
	case hook.SessionStart:
		if req.EnvFile != "" {
			resp.EnvFileAppend = "export PAIR_SESSION_ID=" + sess.ID + "\n"
		}
		w.emit(&pairv1.Event{Type: evSessionChanged, Branch: branch, SessionId: sess.ID})
	case hook.PromptSubmit:
		out.Context, err = w.promptSubmit(h)
	case hook.BeforeTool:
		out, err = w.beforeTool(h)
	case hook.AfterTool:
		err = w.afterTool(h)
	case hook.BeforeStop:
		out.Block, err = w.beforeStop(h)
	case hook.SessionEnd:
		// 会话结束 (含 /clear) 后不会再修改文档, 释放其绑定, 免得一直与新会话冲突.
		if err = w.store.ReleaseBindings(sess.ID); err == nil {
			w.emit(&pairv1.Event{Type: evSessionChanged, Branch: branch, SessionId: sess.ID})
		}
	}
	if err != nil {
		return nil, err
	}
	resp.Output, err = hook.Render(req.Event, out)
	return resp, err
}

// promptSubmit 开启新回合, 在本轮上下文中给出会话短码 (宿主没有会话环境文件时) 与待读取的反馈.
func (w *Workspace) promptSubmit(h *hookCall) (string, error) {
	if err := w.store.ResetTurn(h.sess.ID); err != nil {
		return "", err
	}
	w.emit(&pairv1.Event{Type: evSessionChanged, Branch: h.branch, SessionId: h.sess.ID})
	var lines []string
	if !h.sess.EnvFile {
		lines = append(lines, fmt.Sprintf("Pairwise 会话短码 %s: 本会话执行 pair 命令时都带上 `--session %[1]s`, 如 `pair bind <module> --session %[1]s`; "+
			"回合结束时的检查据此识别本会话的 bind、update 与 skip-update.", h.sess.Code))
	}
	sent, err := w.store.ListFeedback(h.branch, stateTriggered)
	if err != nil {
		return "", err
	}
	if len(sent) > 0 {
		lines = append(lines, fmt.Sprintf("开发者已发送 %d 条反馈待读取 (%s): 先执行 `pair drain%s` 读取, 并在本回合闭环.", len(sent), strings.Join(feedbackIDs(sent), ", "), h.flag()))
	}
	return strings.Join(lines, "\n"), nil
}

// beforeTool 按顺序判断: 立即转向的反馈未读取时拒绝 pair 命令以外的工具调用; 拒绝编辑工具直接写入 .pair/;
// 写入命中 scope 而本会话未绑定的模块时, 每回合每个模块提示一次 pair bind.
func (w *Workspace) beforeTool(h *hookCall) (hook.Output, error) {
	sent, err := w.store.ListFeedback(h.branch, stateTriggered)
	if err != nil {
		return hook.Output{}, err
	}
	if steer := slices.DeleteFunc(sent, func(f *pairv1.Feedback) bool { return !f.Steer }); len(steer) > 0 && !isPairCommand(h.in.Command()) {
		var t strings.Builder
		t.WriteString("开发者发送了要求立即转向的反馈, 读取前暂停其他工具调用:\n")
		for _, f := range steer {
			t.WriteString("- " + feedbackLine(h.ix, f) + "\n")
		}
		fmt.Fprintf(&t, "修复操作: 执行 `pair drain%s` 读取反馈并按其调整计划, 之后的工具调用恢复正常", h.flag())
		return hook.Output{Deny: t.String()}, nil
	}
	rel := w.worktreePath(h.in)
	if rel == "" {
		return hook.Output{}, nil
	}
	if strings.HasPrefix(rel, repo.Dir+"/") {
		return hook.Output{Deny: pairEditReason(h.ix, rel, h.flag())}, nil
	}
	mods := w.scopeModules(h.ix, rel)
	if len(mods) == 0 {
		return hook.Output{}, nil
	}
	bound, err := w.store.Bindings(h.sess.ID)
	if err != nil {
		return hook.Output{}, err
	}
	mods = slices.DeleteFunc(mods, func(m string) bool {
		return slices.ContainsFunc(bound, func(b *store.Binding) bool { return b.DocID == m })
	})
	var hint []string
	if len(mods) > 0 {
		_, err = w.store.UpdateTurn(h.sess.ID, func(t *store.Turn) {
			for _, m := range mods {
				if !slices.Contains(t.Hinted, m) {
					hint = append(hint, m)
					t.Hinted = append(t.Hinted, m)
				}
			}
		})
	}
	if err != nil || len(hint) == 0 {
		return hook.Output{}, err
	}
	return hook.Output{Context: fmt.Sprintf("Pairwise: %s 属于模块 %s, 本会话尚未绑定. 修改前执行 `pair bind %s%s` 并用 `pair show %s` 核对规则; "+
		"只修缺陷、调样式或重构而不改规则时改用 `pair bind %[3]s --preserve \"<原因>\"%[4]s`.",
		rel, strings.Join(hint, ", "), strings.Join(hint, " "), h.flag(), one(hint))}, nil
}

// one 返回提示命令中的模块参数: 只有一个模块时直接给出, 否则用占位符.
func one(mods []string) string {
	if len(mods) == 1 {
		return mods[0]
	}
	return "<module>"
}

// isPairCommand 报告 Bash 命令是否是单条 pair 命令, 立即转向时只放行它们. 含换行、管道、命令分隔、重定向或替换的命令
// 可能夹带其他命令, 一律不放行.
func isPairCommand(cmd string) bool {
	f := strings.Fields(cmd)
	return len(f) > 0 && f[0] == "pair" && !strings.ContainsAny(cmd, "\n;&|<>`$")
}

// pairEditReason 返回拒绝直接编辑 .pair/ 下文件的原因与对应的 pair 命令.
func pairEditReason(ix *repo.Index, rel, flag string) string {
	msg := fmt.Sprintf("不要直接编辑 %s: .pair/ 由 pair 命令读写, 以便三路合并并维护确认状态.\n修复操作: ", rel)
	dir, name := path.Split(strings.TrimPrefix(rel, repo.Dir+"/"))
	id, md := strings.CutSuffix(name, ".md")
	i := slices.IndexFunc(docKinds, func(k doc.Kind) bool { return k.Dir()+"/" == dir })
	switch {
	case !md || i < 0:
		return msg + "该文件没有对应的 pair 命令, 请在回复中说明需要的改动, 由开发者修改"
	case docKinds[i] == doc.KindDecision:
		return msg + fmt.Sprintf("修改问题文档后执行 `pair ask %s --file <doc.md>%s` 重新提交", id, flag)
	case ix.Doc(id) == nil:
		return msg + fmt.Sprintf("执行 `pair update %s --kind %s --file <doc.md>%s` 新建", id, docKinds[i], flag)
	}
	return msg + fmt.Sprintf("执行 `pair show %s` 读取内容与 base, 再执行 `pair update %[1]s#<sec> --base <hash>%s <<'EOF'` 提交单个区块, "+
		"或 `pair update %[1]s --base <hash> --file <doc.md>%[2]s` 提交完整文档", id, flag)
}

var docKinds = []doc.Kind{doc.KindModule, doc.KindFlow, doc.KindDecision}

// afterTool 记录编辑工具改过的源码路径, 并把 Bash 输出中尚未关联会话的 bind_id 关联到当前会话 (路径 ④).
func (w *Workspace) afterTool(h *hookCall) error {
	if rel := w.worktreePath(h.in); rel != "" {
		if strings.HasPrefix(rel, repo.Dir+"/") {
			return nil
		}
		return w.touchTurn(h.sess.ID, func(t *store.Turn) { t.Modified = appendNew(t.Modified, rel) })
	}
	attached := false
	for _, m := range bindIDRe.FindAllStringSubmatch(h.in.Stdout(), -1) {
		err := w.store.AttachBinding(m[1], h.sess.ID)
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return err
		}
		attached = true
	}
	if attached {
		w.emit(&pairv1.Event{Type: evSessionChanged, Branch: h.branch, SessionId: h.sess.ID})
	}
	return nil
}

// stopReason 是 before-stop 的一个拦截原因.
type stopReason struct {
	// key 由判断步骤与涉及的反馈 ID 或模块 ID 组成, 同一 key 在一个回合内只拦截一次.
	key string
	// summary 是一句话概括, 放行时记为会话的待核对原因.
	summary string
	text    string
}

// beforeStop 按三步判断是否拦截本回合结束, 返回拦截原因; 同一原因第二次结束时放行,
// 并把它记为会话的待核对原因. 放行时会话状态置为 idle.
func (w *Workspace) beforeStop(h *hookCall) (string, error) {
	turn, err := w.store.Turn(h.sess.ID)
	if err != nil {
		return "", err
	}
	modules, skips, err := w.modulesReason(h, turn)
	if err != nil {
		return "", err
	}
	reason, err := w.feedbackReason(h, turn)
	if err != nil {
		return "", err
	}
	if reason == nil {
		reason = modules
	}
	release := reason == nil
	if !release {
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(reason.key)))[:12]
		_, err = w.store.UpdateTurn(h.sess.ID, func(t *store.Turn) {
			if release = slices.Contains(t.Blocked, hash); !release {
				t.Blocked = append(t.Blocked, hash)
			}
		})
		if err != nil || !release {
			return reason.text, err
		}
	}
	// 回合结束时, 保持规格的模块的原因登记为跳过原因, 供 prepare-commit-msg 追加 trailer.
	// 拦截期间不登记: 回合继续后仍可能更新这些模块的文档.
	for _, s := range skips {
		if err := w.store.AddSkip(s, h.sess.ID); err != nil {
			return "", err
		}
	}
	var unverified string
	if reason != nil {
		unverified = reason.summary
	}
	if _, err := w.store.UpdateSession(h.sess.ID, func(s *store.Session) { s.State, s.Unverified = "idle", unverified }); err != nil {
		return "", err
	}
	w.emit(&pairv1.Event{Type: evSessionChanged, Branch: h.branch, SessionId: h.sess.ID})
	return "", nil
}

// feedbackReason 是 before-stop 的前两步: 当前分支有已发送尚未读取的反馈; 本回合读取过的反馈尚未闭环.
func (w *Workspace) feedbackReason(h *hookCall, turn *store.Turn) (*stopReason, error) {
	sent, err := w.store.ListFeedback(h.branch, stateTriggered)
	if err != nil {
		return nil, err
	}
	if len(sent) > 0 {
		ids := feedbackIDs(sent)
		var t strings.Builder
		t.WriteString("开发者已发送以下反馈, 尚未读取:\n")
		for _, f := range sent {
			t.WriteString("- " + feedbackLine(h.ix, f) + "\n")
		}
		fmt.Fprintf(&t, "修复操作: 执行 `pair drain%s` 读取, 按反馈修改后在本回合执行 `pair update <module#sec> --base <hash> --resolves <id>%[1]s` 逐条闭环", h.flag())
		return &stopReason{key: "sent:" + strings.Join(ids, ","), summary: "反馈 " + strings.Join(ids, ", ") + " 已发送但未读取", text: t.String()}, nil
	}
	var open []*pairv1.Feedback
	for _, id := range turn.Drained {
		f, err := w.store.Feedback(h.branch, id)
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if f.State == stateTriggered || f.State == stateDrained {
			open = append(open, f)
		}
	}
	if len(open) == 0 {
		return nil, nil
	}
	ids := feedbackIDs(open)
	var t strings.Builder
	t.WriteString("本回合读取的以下反馈尚未闭环:\n")
	for _, f := range open {
		rest := []string{f.Target}
		if f.Kind == kindDecision {
			rest = slices.DeleteFunc(slices.Clone(f.Targets), func(target string) bool { return hasProgress(f.Progress, target) })
		}
		fmt.Fprintf(&t, "- [id: %s] 剩余目标: %s\n", f.Id, strings.Join(rest, ", "))
	}
	fmt.Fprintf(&t, "修复操作: 对剩余目标执行 `pair update <module#sec> --base <hash> --resolves <id>%s`; 问题的某个目标无需改动时执行 "+
		"`pair update <module#sec> --resolves <id> --no-change \"<原因>\"%[1]s`", h.flag())
	return &stopReason{key: "open:" + strings.Join(ids, ","), summary: "反馈 " + strings.Join(ids, ", ") + " 尚未闭环", text: t.String()}, nil
}

// modulesReason 是 before-stop 的第三步: 本回合改过命中 scope 的源码, 而涉及的模块既没有 update 或 ask, 也没有 skip-update.
// 会话对模块的绑定带 --preserve 时不拦截, 返回其原因供登记为跳过原因.
func (w *Workspace) modulesReason(h *hookCall, turn *store.Turn) (*stopReason, []string, error) {
	if turn.Asked || turn.Skipped != "" {
		return nil, nil, nil
	}
	var mods []string
	files := map[string][]string{}
	for _, rel := range turn.Modified {
		for _, m := range w.scopeModules(h.ix, rel) {
			if !slices.Contains(turn.Updated, m) {
				mods = appendNew(mods, m)
				files[m] = append(files[m], rel)
			}
		}
	}
	bound, err := w.store.Bindings(h.sess.ID)
	if err != nil {
		return nil, nil, err
	}
	var skips, block []string
	for _, m := range mods {
		if i := slices.IndexFunc(bound, func(b *store.Binding) bool { return b.DocID == m && b.Preserve != "" }); i >= 0 {
			skips = appendNew(skips, bound[i].Preserve)
		} else {
			block = append(block, m)
		}
	}
	if len(block) == 0 {
		return nil, skips, nil
	}
	var t strings.Builder
	t.WriteString("本回合修改了以下模块 scope 下的源码, 但模块文档未更新:\n")
	for _, m := range block {
		fmt.Fprintf(&t, "- %s: %s\n", m, strings.Join(files[m], ", "))
	}
	fmt.Fprintf(&t, "修复操作: 涉及规则变更时执行 `pair show %s` 核对后用 `pair update %[1]s#<sec> --base <hash>%s` 同步文档; "+
		"确认不涉及规格变更时执行 `pair skip-update --reason \"<原因>\"%[2]s`", one(block), h.flag())
	return &stopReason{key: "modules:" + strings.Join(block, ","), summary: "模块 " + strings.Join(block, ", ") + " 的源码已修改但文档未更新", text: t.String()}, skips, nil
}

// worktreePath 把编辑工具的目标路径转换为相对工作树根目录的路径, 不在工作树内时返回空字符串.
// 相对路径相对宿主调用 Hook 时的工作目录.
func (w *Workspace) worktreePath(in *hook.Input) string {
	p := in.EditPath()
	if p == "" {
		return ""
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(in.Cwd, p)
	}
	rel := w.relPath(p)
	if rel == "" || rel == ".." || strings.HasPrefix(rel, "../") {
		return ""
	}
	return rel
}

// scopeModules 返回 scope 命中源码路径的模块 ID; ignore_globs 中的路径与 check --diff 一样不计入.
func (w *Workspace) scopeModules(ix *repo.Index, rel string) []string {
	if w.config().Ignored(rel) {
		return nil
	}
	var out []string
	for _, e := range ix.ModulesFor(rel) {
		out = append(out, e.ID)
	}
	return out
}

func feedbackIDs(fs []*pairv1.Feedback) []string {
	ids := make([]string, len(fs))
	for i, f := range fs {
		ids[i] = f.Id
	}
	return ids
}

// feedbackLine 返回反馈的单行摘要.
func feedbackLine(ix *repo.Index, f *pairv1.Feedback) string {
	if f.Kind == kindDecision {
		s := fmt.Sprintf("[id: %s] 问题选择方案 %s", f.Id, f.Pick)
		if t := enrich(ix, f).PickTitle; t != "" {
			s += " (" + t + ")"
		}
		return s
	}
	return fmt.Sprintf("[id: %s] 区块 %s 的修改说明: %s", f.Id, f.Target, f.Note)
}
