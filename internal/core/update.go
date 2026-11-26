package core

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"connectrpc.com/connect/v2"
	"go.yaml.in/yaml/v3"

	pairv1 "github.com/FooFishes/pairwise/gen/pair/v1"
	"github.com/FooFishes/pairwise/internal/doc"
	"github.com/FooFishes/pairwise/internal/merge"
	"github.com/FooFishes/pairwise/internal/repo"
	"github.com/FooFishes/pairwise/internal/store"
)

// 问题 target 的闭环进度.
const (
	progressUpdated  = "updated"
	progressNoChange = "no_change"
)

const (
	kindNote     = pairv1.FeedbackKind_FEEDBACK_KIND_NOTE
	kindDecision = pairv1.FeedbackKind_FEEDBACK_KIND_DECISION
)

var kindNames = map[doc.Kind]string{doc.KindModule: "模块", doc.KindFlow: "流程"}

// Update 以基准快照为 base、磁盘当前文件为 ours、按输入构造的文档为 theirs 三路合并写回,
// 并原子闭环 --resolves 关联的反馈. 全部校验在写入之前完成.
func (w *Workspace) Update(ctx context.Context, req *pairv1.UpdateRequest) (*pairv1.UpdateResponse, error) {
	t, err := parseTarget(req.Target)
	if err != nil {
		return nil, err
	}
	if req.SessionId, err = w.sessionID(req.SessionId); err != nil {
		return nil, err
	}
	w.wmu.Lock()
	defer w.wmu.Unlock()
	ix, branch, err := w.sync(ctx)
	if err != nil {
		return nil, err
	}
	fbs, err := w.resolving(ix, branch, req.Resolves)
	if err != nil {
		return nil, err
	}
	var resp *pairv1.UpdateResponse
	if req.NoChange != "" {
		resp, err = w.noChange(ix, branch, t, req, fbs)
	} else {
		resp, err = w.write(ctx, ix, branch, t, req, fbs)
	}
	if err != nil {
		return nil, err
	}
	if err := w.settle(ix, branch, fbs, resp); err != nil {
		return nil, err
	}
	// 写入后会话的绑定基准推进到新 base, 下次省略 --base 时以本次结果为共同祖先.
	if req.SessionId != "" && resp.Base != "" {
		advanced, err := w.store.AdvanceBinding(req.SessionId, t.Doc, resp.Base)
		if err != nil {
			return nil, err
		}
		if advanced {
			w.emit(&pairv1.Event{Type: evSessionChanged, Branch: branch, SessionId: req.SessionId})
		}
	}
	// --no-change 不写文档, 不算本回合更新过该文档.
	if req.NoChange == "" {
		err = w.touchTurn(req.SessionId, func(tu *store.Turn) { tu.Updated = appendNew(tu.Updated, t.Doc) })
	}
	return resp, err
}

// resolving 查找 --resolves 关联的反馈, 只接受开发者已发送且尚未闭环的条目.
func (w *Workspace) resolving(ix *repo.Index, branch string, ids []string) ([]*pairv1.Feedback, error) {
	var out []*pairv1.Feedback
	for _, id := range appendNew(nil, ids...) {
		fb, err := w.store.Feedback(branch, id)
		if isNotFound(err) && ix.Decision(id) != nil {
			return nil, undecided(id)
		}
		if isNotFound(err) {
			return nil, connect.NewError(connect.CodeNotFound,
				fmt.Sprintf("反馈「%s」不存在.\n修复操作: 执行 `pair drain` 查看已发送反馈的 ID 后重试", id))
		}
		if err != nil {
			return nil, err
		}
		switch fb.State {
		case pairv1.FeedbackState_FEEDBACK_STATE_TRIGGERED, pairv1.FeedbackState_FEEDBACK_STATE_DRAINED:
			out = append(out, fb)
		case pairv1.FeedbackState_FEEDBACK_STATE_RESOLVED:
			// 闭环会删除问题文件; 问题文件仍在说明它以同一 ID 重新提交, 是尚未确认的新一轮问题.
			if fb.Kind == kindDecision && ix.Decision(id) != nil {
				return nil, undecided(id)
			}
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				fmt.Sprintf("反馈「%s」已闭环, 不能再次关联.\n修复操作: 去掉 `--resolves %[1]s` 后重试", id))
		default:
			return nil, undecided(id)
		}
	}
	return out, nil
}

func undecided(id string) error {
	return connect.NewError(connect.CodeFailedPrecondition,
		fmt.Sprintf("反馈「%s」开发者尚未确认, 不能闭环.\n修复操作: 等开发者在工作台选择并点击 Trigger 发送后, 执行 `pair drain` 读取再闭环", id))
}

// noChange 把问题的单个目标标记为已核对且无需改动, 不写正文.
func (w *Workspace) noChange(ix *repo.Index, branch string, t doc.Target, req *pairv1.UpdateRequest, fbs []*pairv1.Feedback) (*pairv1.UpdateResponse, error) {
	switch {
	case len(fbs) == 0:
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Sprintf("--no-change 必须与 --resolves 一起使用, 用来声明问题的某个目标无需改动.\n修复操作: 执行 `pair update %s --resolves <问题 ID> --no-change \"<原因>\"`", t))
	case t.Section == "":
		return nil, connect.NewError(connect.CodeInvalidArgument,
			"--no-change 只能用于单个目标区块.\n修复操作: 对问题的每个目标分别执行 `pair update <module#sec> --resolves <问题 ID> --no-change \"<原因>\"`")
	}
	for _, fb := range fbs {
		if fb.Kind != kindDecision {
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				fmt.Sprintf("批注「%s」不能以 --no-change 结案, 批注须由修改其目标区块的更新闭环.\n"+
					"修复操作: 执行 `pair show %[2]s` 读取区块最新内容与 base, 按批注修改 (已符合批注时原样提交) 后执行 `pair update %[2]s --base <hash> --resolves %[1]s <<'EOF'`", fb.Id, fb.Target))
		}
		if !slices.ContainsFunc(fb.Targets, func(target string) bool { return covers(t, target) }) {
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				fmt.Sprintf("「%s」不是问题「%s」的目标, 其 targets 为 %s.\n修复操作: 对其中的目标执行 `pair update <module#sec> --resolves %[2]s --no-change \"<原因>\"`",
					t, fb.Id, strings.Join(fb.Targets, ", ")))
		}
		if slices.ContainsFunc(fb.Progress, func(p *pairv1.TargetProgress) bool { return p.Status == progressUpdated && covers(t, p.Target) }) {
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				fmt.Sprintf("问题「%s」的目标「%s」已经更新过, 不能再标记为无需改动.\n修复操作: 无需处理, 该目标已计入完成度; 执行 `pair stage list` 查看问题的剩余目标", fb.Id, t))
		}
	}
	for _, fb := range fbs {
		for _, target := range fb.Targets {
			if covers(t, target) {
				if err := w.store.SetProgress(branch, fb.Id, target, progressNoChange, req.NoChange); err != nil {
					return nil, err
				}
			}
		}
	}
	resp := &pairv1.UpdateResponse{DocId: t.Doc}
	if e := ix.Doc(t.Doc); e != nil {
		resp.Path = e.Rel
	}
	return resp, nil
}

// write 合并输入并写回文件, 再记录 --resolves 问题的 updated 进度.
func (w *Workspace) write(ctx context.Context, ix *repo.Index, branch string, t doc.Target, req *pairv1.UpdateRequest, fbs []*pairv1.Feedback) (*pairv1.UpdateResponse, error) {
	if err := checkCoverage(t, fbs); err != nil {
		return nil, err
	}
	resp := &pairv1.UpdateResponse{DocId: t.Doc}
	var ours *doc.Document
	var kind doc.Kind
	var path string
	var oursRaw, raw []byte
	var err error
	switch e := ix.Doc(t.Doc); {
	case e == nil:
		kind, raw, err = created(t, req)
		path, resp.Created = w.repo.Path(kind, t.Doc), true
	case e.Doc == nil || merge.Mergeable(e.Doc) != nil:
		kind, path = e.Kind, e.Path
		ours, oursRaw, raw, err = overwrite(e, t, req)
	default:
		ours, kind, path, oursRaw = e.Doc, e.Kind, e.Path, e.Doc.Raw
		raw, err = w.merged(ctx, e, t, req)
	}
	if err != nil {
		return nil, err
	}

	result, err := doc.Parse(raw, kind)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Sprintf("输入无法解析: %v.\n修复操作: 按上述原因修正输入后重试", err))
	}
	if control(result, ours, fbs, resp) {
		raw = render(result)
	}
	resp.Path = w.repo.Rel(path)
	if resp.Changed = resp.Created || !bytes.Equal(raw, oursRaw); resp.Changed {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
		if err := repo.WriteFileAtomic(path, raw); err != nil {
			return nil, err
		}
		resp.Diff = merge.Diff("a/"+resp.Path, "b/"+resp.Path, oursRaw, raw)
	}
	if resp.Base, err = w.store.PutBase(raw); err != nil {
		return nil, err
	}
	for _, fb := range fbs {
		for _, target := range fb.Targets {
			if fb.Kind == kindDecision && covers(t, target) {
				if err := w.store.SetProgress(branch, fb.Id, target, progressUpdated, ""); err != nil {
					return nil, err
				}
			}
		}
	}
	return resp, nil
}

// created 构造新建文档: 只接受整文档输入.
func created(t doc.Target, req *pairv1.UpdateRequest) (doc.Kind, []byte, error) {
	kind, err := newKind(t, req.Kind)
	if err != nil {
		return 0, nil, err
	}
	raw, err := fresh(t, kind, req.Content, nil)
	return kind, raw, err
}

// overwrite 以整文档输入覆盖无法按区块合并的已有文件 (无法解析, 或区块 ID 重复而无法对齐). 不做合并,
// 因此 --base 必须等于当前文件的整文件哈希, 表明调用方读过当前内容. 返回受控字段的来源、当前文件与覆盖后的内容:
// 无法解析的文件只有 Frontmatter 可读时 (如正文含合并冲突) 以它为来源, 都不可读时为 nil.
func overwrite(e *repo.Entry, t doc.Target, req *pairv1.UpdateRequest) (ours *doc.Document, cur, raw []byte, err error) {
	if cur, err = os.ReadFile(e.Path); err != nil {
		return nil, nil, nil, err
	}
	if t.Section != "" || req.Base != doc.FileHash(cur) {
		reason := e.Err
		if reason == nil {
			reason = merge.Mergeable(e.Doc)
		}
		return nil, nil, nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Sprintf(
			"拒绝更新: %s 无法按区块合并 (%v), 只能以完整文档覆盖, 且 --base 须等于当前文件的整文件哈希.\n"+
				"修复操作: 执行 `pair show %s` 查看当前内容与 base, 再执行 `pair update %[3]s --base <hash> <<'EOF'` (或 `--file <doc.md>`) 传入修正后的完整文档 (含 Frontmatter)",
			e.Rel, reason, e.ID))
	}
	ours = e.Doc
	if ours == nil && e.Front != nil {
		ours = &doc.Document{Front: e.Front}
	}
	// 当前 Frontmatter 可读时, 不带 Frontmatter 的输入多半是漏写了, 覆盖会丢掉 id、scope 与确认状态.
	if in, perr := doc.Parse([]byte(req.Content), e.Kind); ours != nil && perr == nil && in.FrontEnd == 0 {
		return nil, nil, nil, connect.NewError(connect.CodeInvalidArgument, fmt.Sprintf(
			"整文档覆盖的输入缺少 Frontmatter, 覆盖后会丢掉 id、scope 与确认状态.\n"+
				"修复操作: 以 `pair show %s` 输出中的 Frontmatter 为底, 执行 `pair update %[1]s --base %s <<'EOF'` (或 `--file <doc.md>`) 传入完整文档", e.ID, req.Base))
	}
	// 以当前文件可读的 Frontmatter 为 base, 原样提交的受控字段不算改动, Frontmatter 字节保持不变.
	raw, err = fresh(t, e.Kind, req.Content, ours)
	return ours, cur, raw, err
}

// fresh 按整文档输入构造不经合并的内容, 受控字段设为 base 的值 (新建时 base 为 nil, 清空).
func fresh(t doc.Target, kind doc.Kind, content string, base *doc.Document) ([]byte, error) {
	theirs, dirty, err := wholeTheirs(t, kind, content, base)
	if err != nil {
		return nil, err
	}
	if dirty {
		return render(theirs), nil
	}
	return theirs.Raw, nil
}

// merged 以基准快照为 base、磁盘当前文件为 ours、按输入构造的文档为 theirs 三路合并.
func (w *Workspace) merged(ctx context.Context, e *repo.Entry, t doc.Target, req *pairv1.UpdateRequest) ([]byte, error) {
	base, err := w.base(ctx, e, t, req)
	if err != nil {
		return nil, err
	}
	var theirs *doc.Document
	if t.Section == "" {
		theirs, _, err = wholeTheirs(t, e.Kind, req.Content, base)
	} else {
		theirs, err = sectionTheirs(t, base, req.Content)
	}
	if err != nil {
		return nil, err
	}
	// 当前文件已确认可对齐, 失败只会来自输入或 base 中重复的区块.
	res, err := merge.Merge3(base, e.Doc, theirs, merge.Labels{Base: "base", Ours: "当前文件", Theirs: "本次更新"})
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Sprintf(
			"%v.\n修复操作: 去掉输入中重复的区块后重试; 仍然失败时执行 `pair show %s` 获取新的 base, 在最新内容基础上重新执行 `pair update %s --base <hash>`", err, e.ID, t))
	}
	if len(res.Conflicts) > 0 {
		return nil, conflictErr(t, res.Conflicts)
	}
	return res.Raw, nil
}

// checkCoverage 校验本次更新覆盖了每条 --resolves 反馈的目标.
func checkCoverage(t doc.Target, fbs []*pairv1.Feedback) error {
	for _, fb := range fbs {
		switch fb.Kind {
		case kindNote:
			if !covers(t, fb.Target) {
				return connect.NewError(connect.CodeFailedPrecondition,
					fmt.Sprintf("批注「%s」针对区块「%s」, 本次更新「%s」没有覆盖它.\n修复操作: 执行 `pair show %[2]s` 读取区块最新内容与 base, 修改后执行 `pair update %[2]s --base <hash> --resolves %[1]s <<'EOF'`", fb.Id, fb.Target, t))
			}
		case kindDecision:
			if !slices.ContainsFunc(fb.Targets, func(s string) bool { return covers(t, s) }) {
				return connect.NewError(connect.CodeFailedPrecondition,
					fmt.Sprintf("本次更新「%s」没有覆盖问题「%s」的任何目标, 其 targets 为 %s.\n修复操作: 对每个目标执行 `pair update <module#sec> --base <hash> --resolves %[2]s`, 无需改动的目标改用 `--no-change \"<原因>\"`",
						t, fb.Id, strings.Join(fb.Targets, ", ")))
			}
		}
	}
	return nil
}

// covers 报告本次更新是否覆盖区块 target: 整文档更新覆盖该文档的全部区块.
func covers(t doc.Target, target string) bool {
	x, err := doc.ParseTarget(target)
	return err == nil && x.Doc == t.Doc && (t.Section == "" || x.Section == t.Section)
}

// newKind 返回新建文档的类型; 新建只接受整文档输入.
func newKind(t doc.Target, kind string) (doc.Kind, error) {
	if t.Section != "" {
		return 0, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Sprintf("文档「%s」不存在, 不能按区块更新.\n修复操作: 执行 `pair update %[1]s --kind module <<'EOF'` (或 `--file <doc.md>`) 传入含 Frontmatter 的完整文档新建, 流程文档改用 `--kind flow`", t.Doc))
	}
	switch kind {
	case "", "module":
		return doc.KindModule, nil
	case "flow":
		return doc.KindFlow, nil
	}
	return 0, connect.NewError(connect.CodeInvalidArgument,
		fmt.Sprintf("--kind %q 不合法.\n修复操作: 改为 `--kind module` 或 `--kind flow`", kind))
}

// base 读取本次合并的共同祖先: 取 --base, 为空时取会话对该文档的有效绑定.
// 本地快照未命中时沿该文件的 Git 历史查找哈希匹配的版本.
func (w *Workspace) base(ctx context.Context, e *repo.Entry, t doc.Target, req *pairv1.UpdateRequest) (*doc.Document, error) {
	hash := req.Base
	if hash == "" && req.SessionId != "" {
		bs, err := w.store.Bindings(req.SessionId)
		if err != nil {
			return nil, err
		}
		for _, b := range bs {
			if b.DocID == e.ID {
				hash = b.Base
			}
		}
	}
	if hash == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Sprintf(
			"拒绝更新: %s `%s` 已存在, 但本次调用未提供基准哈希 `--base`, 直接写入可能覆盖已有内容.\n"+
				"修复操作: 请先执行 `pair show %[2]s` 读取最新内容并获取文件末尾输出的 base 哈希, 再重新执行 `pair update %s --base <hash> <<'EOF'`.",
			kindNames[e.Kind], e.ID, t))
	}
	raw, err := w.store.GetBase(hash)
	if isNotFound(err) {
		err = w.walkHistory(ctx, e.Rel, func(r []byte) bool {
			raw = r
			return doc.FileHash(r) == hash
		})
	}
	if isNotFound(err) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Sprintf(
			"拒绝更新: 基准哈希 `%s` 在本地快照与 %s 的 Git 历史中都不存在, 无法确定合并的共同祖先.\n"+
				"修复操作: 请先执行 `pair show %s` 获取新的 base 哈希, 在最新内容基础上重新执行 `pair update %s --base <hash>`.",
			hash, e.Rel, e.ID, t))
	}
	if err != nil {
		return nil, err
	}
	d, err := doc.Parse(raw, e.Kind)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Sprintf(
			"拒绝更新: 基准哈希 `%s` 对应的版本无法解析 (%v), 不能作为合并的共同祖先.\n"+
				"修复操作: 请先执行 `pair show %s` 获取新的 base 哈希, 在最新内容基础上重新执行 `pair update %s --base <hash>`.",
			hash, err, e.ID, t))
	}
	return d, nil
}

// wholeTheirs 解析整文档输入: 补齐或校验 id, 并把受控字段设回 base 的值 (新建时清空).
// 返回 Frontmatter 是否被改动.
func wholeTheirs(t doc.Target, kind doc.Kind, content string, base *doc.Document) (*doc.Document, bool, error) {
	d, err := doc.Parse([]byte(content), kind)
	if err != nil {
		return nil, false, connect.NewError(connect.CodeInvalidArgument,
			fmt.Sprintf("输入无法解析: %v.\n修复操作: 按上述原因修正输入后重试", err))
	}
	if base != nil && d.FrontEnd == 0 {
		return nil, false, connect.NewError(connect.CodeInvalidArgument, fmt.Sprintf(
			"整文档更新的输入缺少 Frontmatter, 合并时会被视为删除 id、scope 与 sections.\n"+
				"修复操作: 以 `pair show %s` 的完整输出为底修改后提交, 或改用 `pair update %[1]s#<sec> --base <hash>` 只更新单个区块", t.Doc))
	}
	dirty := false
	switch d.Front.ID {
	case t.Doc:
	case "":
		setID(d.Front, t.Doc)
		dirty = true
	default:
		return nil, false, connect.NewError(connect.CodeInvalidArgument,
			fmt.Sprintf("输入 Frontmatter 的 id「%s」与目标「%s」不一致.\n修复操作: 把 Frontmatter 改为 `id: %[2]s` 后重试", d.Front.ID, t.Doc))
	}
	var order []string
	if base != nil {
		order = base.Front.SectionOrder
	}
	for _, id := range union(order, d.Front.SectionOrder) {
		bm := metaOf(base, id)
		m := doc.SectionMeta{ConfirmedHash: bm.ConfirmedHash, Assumptions: metaOf(d, id).Assumptions, Retracted: bm.Retracted}
		dirty = setMeta(d.Front, id, m) || dirty
	}
	return d, dirty, nil
}

// sectionTheirs 以 base 为底替换目标区块: 输入不含标题行时保留原标题, 区块不存在时追加为新区块;
// 输入带 assumptions 时整体替换该区块的假设.
func sectionTheirs(t doc.Target, base *doc.Document, content string) (*doc.Document, error) {
	in, err := doc.ParseSectionInput([]byte(content))
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Sprintf("区块输入无法解析: %v.\n修复操作: 区块输入只能以只含 `assumptions:` 的 Frontmatter 开头, 其后是可选的 `## 标题 <!-- #%s -->` 与正文", err, t.Section))
	}
	switch {
	case in.Heading != nil && in.HeadingID == "":
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Sprintf("输入首行的二级标题缺少锚点.\n修复操作: 改为 `## <标题> <!-- #%s -->`, 或去掉标题行只提交正文以保留原标题", t.Section))
	case in.Heading != nil && in.HeadingID != t.Section:
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Sprintf("输入标题的锚点「%s」与目标区块「%s」不一致.\n修复操作: 改为 `<!-- #%[2]s -->`; 如需更改区块 ID, 改用整文档更新 `pair update %s --base <hash>`", in.HeadingID, t.Section, t.Doc))
	}
	front := base.Raw[:base.FrontEnd]
	want := outline(base)
	var raw []byte
	switch s := base.Section(t.Section); {
	case s != nil:
		heading, body := in.Heading, in.Body
		if heading == nil {
			orig := base.Raw[s.Body.Start:s.Span.End]
			// 只设置假设、不带正文的输入保留原正文; 什么都没有的输入多半是漏了 heredoc, 不能清空正文.
			switch {
			case len(bytes.TrimSpace(body)) > 0:
			case in.HasAssumptions:
				body = orig
			default:
				return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Sprintf(
					"区块输入为空, 未写入.\n修复操作: 执行 `pair show %s` 读取区块后, 把修改后的正文经 stdin (`<<'EOF'`) 或 --file 传给 `pair update %[1]s --base <hash>`", t))
			}
			heading, body = base.Raw[s.Span.Start:s.Body.Start], keepSeparator(orig, body)
		}
		sec := keepEnding(base.Text(s.Span), slices.Concat(heading, body))
		raw = doc.Assemble(front, base.Raw[base.FrontEnd:s.Span.Start], sec, base.Raw[s.Span.End:])
	case in.Heading == nil:
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Sprintf("区块「%s」不存在, 新建区块的输入必须以标题行开头.\n修复操作: 在输入首行写 `## <标题> <!-- #%s -->` 后重试", t, t.Section))
	default:
		want = append(want, t.Section)
		raw = doc.Assemble(front, blankLineAfter(base.Raw[base.FrontEnd:]), slices.Concat(in.Heading, in.Body))
	}
	d, err := doc.Parse(raw, base.Kind)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Sprintf("区块输入无法解析: %v.\n修复操作: 按上述原因修正区块输入后重试", err))
	}
	// 输入中未闭合的代码块、HTML 注释或夹带的 ## 标题会吞掉或拆分其后的区块, 合并时将被当作删除.
	if got := outline(d); !slices.Equal(got, want) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Sprintf(
			"区块输入改变了文档的区块切分, 未写入: 期望区块为 %s, 按本次输入拼接后为 %s. "+
				"输入中可能有未闭合的 ``` 代码块或 `<!--` 注释吞掉了其后的区块, 或夹带了其他 `##` 标题.\n"+
				"修复操作: 闭合输入中的代码块与注释, 只提交区块 #%s 自身的内容, 再重新执行 `pair update %s --base <hash>`",
			strings.Join(want, ", "), cmp.Or(strings.Join(got, ", "), "(无)"), t.Section, t))
	}
	if in.HasAssumptions {
		m := metaOf(d, t.Section)
		m.Assumptions = in.Assumptions
		setMeta(d.Front, t.Section, m)
	}
	return d, nil
}

// outline 返回文档的区块序列, 缺锚点的区块以标题代替.
func outline(d *doc.Document) []string {
	var out []string
	for _, s := range d.Sections {
		out = append(out, cmp.Or(s.ID, "## "+s.Title))
	}
	return out
}

// keepSeparator 在只替换正文的输入不以空行开头时, 沿用原正文开头的空行, 使标题与正文之间的
// 分隔不被当作改动 (stdin 输入 `<<'EOF'` 通常直接从正文第一行写起).
func keepSeparator(orig, body []byte) []byte {
	first, _, _ := bytes.Cut(body, []byte("\n"))
	if len(bytes.TrimSpace(first)) == 0 {
		return body
	}
	n := 0
	for n < len(orig) {
		i := bytes.IndexByte(orig[n:], '\n')
		if i < 0 || len(bytes.TrimSpace(orig[n:n+i])) > 0 {
			break
		}
		n += i + 1
	}
	return slices.Concat(orig[:n], body)
}

// keepEnding 让替换后的区块沿用原区块末尾的换行与空行, 使与下一区块之间的分隔不被当作改动.
func keepEnding(orig, sec []byte) []byte {
	end := bytes.TrimRight(orig, "\r\n")
	return slices.Concat(bytes.TrimRight(sec, "\r\n"), orig[len(end):])
}

// blankLineAfter 让 b 以一个空行结束, 使后面拼接的区块标题与前文之间隔开.
func blankLineAfter(b []byte) []byte {
	return append(bytes.TrimRight(b, "\n"), "\n\n"...)
}

// control 把合并结果的受控字段设为 ours 的值, 移除 --resolves 批注针对的假设,
// 把 ours 中存在、结果中消失且未被关联的假设追加到同区块 retracted, 并丢弃已不存在区块的元数据.
// 返回 Frontmatter 是否被改动.
func control(result, ours *doc.Document, fbs []*pairv1.Feedback, resp *pairv1.UpdateResponse) bool {
	removed := map[string][]string{}
	for _, fb := range fbs {
		if fb.Kind == kindNote && fb.AssumptionId != "" {
			nt, _ := doc.ParseTarget(fb.Target)
			removed[nt.Section] = append(removed[nt.Section], fb.AssumptionId)
		}
	}
	var order []string
	if ours != nil {
		order = ours.Front.SectionOrder
	}
	dirty := false
	for _, id := range union(result.Front.SectionOrder, order) {
		var m doc.SectionMeta
		if result.Section(id) != nil {
			om, rm := metaOf(ours, id), metaOf(result, id)
			for _, aid := range removed[id] {
				if hasAssumption(om.Assumptions, aid) || hasAssumption(rm.Assumptions, aid) {
					resp.RemovedAssumptions = append(resp.RemovedAssumptions, aid)
				}
			}
			m.ConfirmedHash = om.ConfirmedHash
			m.Assumptions = slices.DeleteFunc(slices.Clone(rm.Assumptions), func(a doc.Assumption) bool {
				return slices.Contains(removed[id], a.ID)
			})
			m.Retracted = slices.Clone(om.Retracted)
			for _, a := range om.Assumptions {
				if slices.Contains(removed[id], a.ID) || hasAssumption(m.Assumptions, a.ID) || hasAssumption(m.Retracted, a.ID) {
					continue
				}
				m.Retracted = append(m.Retracted, a)
				resp.Retracted = append(resp.Retracted, &pairv1.Assumption{Id: a.ID, Text: a.Text})
			}
		}
		dirty = setMeta(result.Front, id, m) || dirty
	}
	return dirty
}

// settle 闭环批注与全部 targets 都已有进度的问题 (删除其问题文件), 然后统一发出事件.
func (w *Workspace) settle(ix *repo.Index, branch string, fbs []*pairv1.Feedback, resp *pairv1.UpdateResponse) error {
	for _, fb := range fbs {
		if fb.Kind == kindDecision {
			cur, err := w.store.Feedback(branch, fb.Id)
			if err != nil {
				return err
			}
			if slices.ContainsFunc(cur.Targets, func(t string) bool { return !hasProgress(cur.Progress, t) }) {
				resp.OpenDecisions = append(resp.OpenDecisions, enrich(ix, cur))
				continue
			}
			if err := os.Remove(w.repo.Path(doc.KindDecision, fb.Id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			resp.DeletedDecisions = append(resp.DeletedDecisions, fb.Id)
		}
		if err := w.store.Resolve(branch, fb.Id); err != nil {
			return err
		}
		resp.Resolved = append(resp.Resolved, fb.Id)
	}
	if _, err := w.reload(); err != nil {
		return err
	}
	if len(resp.DeletedDecisions) > 0 {
		w.emit(&pairv1.Event{Type: evDecisionClosed, Branch: branch, Ids: resp.DeletedDecisions})
	}
	if len(resp.Resolved) > 0 {
		w.emit(&pairv1.Event{Type: evResolved, Branch: branch, Ids: resp.Resolved})
	}
	return nil
}

func hasProgress(ps []*pairv1.TargetProgress, target string) bool {
	return slices.ContainsFunc(ps, func(p *pairv1.TargetProgress) bool { return p.Target == target })
}

func conflictErr(t doc.Target, cs []merge.Conflict) error {
	var where, diffs []string
	for _, c := range cs {
		where = append(where, c.Where)
		diffs = append(diffs, merge.Diff("当前文件", "本次更新", c.Ours, c.Theirs))
	}
	return connect.NewError(connect.CodeAborted, fmt.Sprintf(
		"拒绝更新: 「%s」在 base 之后已被修改, 以下位置与本次更新冲突: %s.\n"+
			"修复操作: 请执行 `pair show %s` 重新读取最新内容与 base, 在其基础上合入本次改动后重新执行 `pair update %[3]s --base <hash>`.\n\n"+
			"冲突差异 (当前文件 -> 本次更新):\n%s",
		t.Doc, strings.Join(where, ", "), t, strings.Join(diffs, "\n")))
}

// render 按当前 Frontmatter 节点写回文档: 未改动的键沿用原文字节, 正文字节不变.
func render(d *doc.Document) []byte {
	return slices.Concat(d.Front.Render(d.Raw[:d.FrontEnd]), d.Raw[d.FrontEnd:])
}

// setID 把 Frontmatter 的 id 设为 id; 没有 id 键时插在最前.
func setID(f *doc.Front, id string) {
	f.ID = id
	v := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: id}
	for i := 0; i+1 < len(f.Node.Content); i += 2 {
		if f.Node.Content[i].Value == "id" {
			f.Node.Content[i+1] = v
			return
		}
	}
	f.Node.Content = slices.Insert(f.Node.Content, 0, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "id"}, v)
}

// metaOf 返回区块元数据的副本, d 为空或没有元数据时返回零值.
func metaOf(d *doc.Document, id string) doc.SectionMeta {
	if d != nil {
		if m := d.Meta(id); m != nil {
			return *m
		}
	}
	return doc.SectionMeta{}
}

// setMeta 在元数据有变化时写入 Frontmatter 节点, 报告是否有变化. 未变化时不动节点, 以保留原字节.
func setMeta(f *doc.Front, id string, m doc.SectionMeta) bool {
	var old doc.SectionMeta
	if o := f.Sections[id]; o != nil {
		old = *o
	}
	if old.ConfirmedHash == m.ConfirmedHash && slices.Equal(old.Assumptions, m.Assumptions) && slices.Equal(old.Retracted, m.Retracted) {
		return false
	}
	f.SetMeta(id, &m)
	return true
}

func hasAssumption(as []doc.Assumption, id string) bool {
	return slices.ContainsFunc(as, func(a doc.Assumption) bool { return a.ID == id })
}

func union(a, b []string) []string {
	return appendNew(slices.Clone(a), b...)
}
