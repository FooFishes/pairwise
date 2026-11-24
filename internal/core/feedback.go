package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect/v2"

	pairv1 "github.com/FooFishes/pairwise/gen/pair/v1"
	"github.com/FooFishes/pairwise/internal/check"
	"github.com/FooFishes/pairwise/internal/doc"
	"github.com/FooFishes/pairwise/internal/repo"
	"github.com/FooFishes/pairwise/internal/store"
)

// Ask 校验并写入或覆盖 .pair/decisions/<id>.md.
func (w *Workspace) Ask(_ context.Context, req *pairv1.AskRequest) (*pairv1.AskResponse, error) {
	t, err := parseTarget(req.Id)
	if err != nil {
		return nil, err
	}
	if t.Section != "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Sprintf("问题 ID %q 不能带 #.\n修复操作: 执行 `pair ask <id> --file <doc.md>`, id 只能包含字母、数字、- 与 _", req.Id))
	}
	w.wmu.Lock()
	defer w.wmu.Unlock()
	ix, err := w.reload()
	if err != nil {
		return nil, err
	}
	d, err := doc.Parse([]byte(req.Content), doc.KindDecision)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Sprintf("问题文档无法解析: %v.\n修复操作: 修正 Frontmatter 后执行 `pair ask %s --file <doc.md>` 重新提交", err, req.Id))
	}
	dirty := false
	switch d.Front.ID {
	case req.Id:
	case "":
		setID(d.Front, req.Id)
		dirty = true
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Sprintf("问题文档 Frontmatter 的 id「%s」与「%s」不一致.\n修复操作: 把 Frontmatter 改为 `id: %[2]s` 后重新提交", d.Front.ID, req.Id))
	}
	var errs []string
	for _, dg := range check.Doc(ix, &repo.Entry{Kind: doc.KindDecision, ID: req.Id, Doc: d}) {
		if dg.Severity == doc.SeverityError {
			errs = append(errs, fmt.Sprintf("- 第 %d 行: %s\n  修复操作: %s", dg.Line, dg.Message, dg.Fix))
		}
	}
	if len(errs) > 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Sprintf("问题「%s」未通过校验, 未写入:\n%s", req.Id, strings.Join(errs, "\n")))
	}
	raw := d.Raw
	if dirty {
		raw = render(d)
	}
	path := w.repo.Path(doc.KindDecision, req.Id)
	_, statErr := os.Stat(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if err := repo.WriteFileAtomic(path, raw); err != nil {
		return nil, err
	}
	if _, err := w.reload(); err != nil {
		return nil, err
	}
	_, branch := w.snapshot()
	dec, err := w.toDecision(branch, req.Id, d)
	if err != nil {
		return nil, err
	}
	if err := w.touchTurn(req.SessionId, func(tu *store.Turn) { tu.Asked = true }); err != nil {
		return nil, err
	}
	return &pairv1.AskResponse{Id: req.Id, Path: w.repo.Rel(path), Replaced: statErr == nil, Decision: dec, UiUrl: w.uiURLValue()}, nil
}

// WaitDecision 等待开发者对问题点击 Trigger: 等到时立即标记为已读取并返回所选方案,
// 超时返回 decided=false 与默认方案. 通过事件订阅唤醒, 每次唤醒后以存储中的状态为准.
func (w *Workspace) WaitDecision(ctx context.Context, req *pairv1.WaitDecisionRequest) (*pairv1.WaitDecisionResponse, error) {
	ix, branch := w.snapshot()
	e := ix.Decision(req.Id)
	if e == nil || e.Doc == nil {
		return nil, connect.NewError(connect.CodeNotFound,
			fmt.Sprintf("问题「%s」不存在或无法解析.\n修复操作: 先执行 `pair ask %[1]s --file <doc.md>` 提交问题", req.Id))
	}
	secs := req.TimeoutSecs
	if secs <= 0 {
		secs = int32(w.repo.Config.AskWaitTimeoutSecs)
	}
	resp := &pairv1.WaitDecisionResponse{DefaultOption: e.Doc.Front.DefaultOption, TimeoutSecs: secs}
	timer := time.NewTimer(time.Duration(secs) * time.Second)
	defer timer.Stop()
	events, cancel := w.Subscribe()
	defer func() { cancel() }()
	for {
		fb, err := w.store.Feedback(branch, req.Id)
		if err != nil && !isNotFound(err) {
			return nil, err
		}
		if fb.GetState() == pairv1.FeedbackState_FEEDBACK_STATE_TRIGGERED {
			if err := w.store.MarkDrained(branch, req.Id, req.SessionId); err != nil {
				return nil, err
			}
			if fb, err = w.store.Feedback(branch, req.Id); err != nil {
				return nil, err
			}
			w.emit(&pairv1.Event{Type: evDrained, Branch: branch, Ids: []string{req.Id}, SessionId: req.SessionId})
			resp.Decided, resp.Feedback = true, enrich(ix, fb)
			err = w.touchTurn(req.SessionId, func(tu *store.Turn) { tu.Drained = appendNew(tu.Drained, req.Id) })
			return resp, err
		}
		for wake := false; !wake; {
			select {
			case ev, ok := <-events:
				// 订阅者跟不上时通道被关闭, 重新订阅后回到存储检查, 不会漏掉期间的 Trigger.
				if !ok {
					events, cancel = w.Subscribe()
				}
				wake = !ok || ev.Type == evTrigger && slices.Contains(ev.Ids, req.Id)
			case <-timer.C:
				return resp, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
}

// Drain 读取全部已发送的反馈并标记为已读取.
func (w *Workspace) Drain(_ context.Context, req *pairv1.DrainRequest) (*pairv1.DrainResponse, error) {
	ix, branch := w.snapshot()
	items, err := w.store.Drain(branch, req.SessionId)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, f := range items {
		enrich(ix, f)
		ids = append(ids, f.Id)
	}
	if len(ids) > 0 {
		w.emit(&pairv1.Event{Type: evDrained, Branch: branch, Ids: ids, SessionId: req.SessionId})
	}
	err = w.touchTurn(req.SessionId, func(tu *store.Turn) { tu.Drained = appendNew(tu.Drained, ids...) })
	return &pairv1.DrainResponse{Items: items}, err
}

// StageDecide 暂存对问题的方案选择.
func (w *Workspace) StageDecide(_ context.Context, req *pairv1.StageDecideRequest) (*pairv1.StageDecideResponse, error) {
	ix, branch := w.snapshot()
	e := ix.Decision(req.DecisionId)
	if e == nil || e.Doc == nil {
		return nil, connect.NewError(connect.CodeNotFound,
			fmt.Sprintf("问题「%s」不存在或无法解析.\n修复操作: 执行 `pair list decisions` 查看待确认问题", req.DecisionId))
	}
	if option(e.Doc, req.Pick) == nil {
		var ids []string
		for _, c := range compares(e.Doc) {
			for _, it := range c.Items {
				ids = append(ids, it.ID)
			}
		}
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Sprintf("「%s」不是问题「%s」的方案, 可选方案: %s.\n修复操作: 执行 `pair stage decide %[2]s --pick <方案 ID>`",
				req.Pick, req.DecisionId, strings.Join(ids, ", ")))
	}
	fb, err := w.store.StageDecision(branch, req.DecisionId, req.Pick, req.Note, e.Doc.Front.Targets)
	if err != nil {
		return nil, storeErr(err, "")
	}
	w.emit(&pairv1.Event{Type: evStageChanged, Branch: branch, Ids: []string{fb.Id}})
	return &pairv1.StageDecideResponse{Item: enrich(ix, fb)}, nil
}

// StageNote 暂存针对区块或其中某条假设的修改说明.
func (w *Workspace) StageNote(_ context.Context, req *pairv1.StageNoteRequest) (*pairv1.StageNoteResponse, error) {
	ix, branch := w.snapshot()
	e, s, err := findSection(ix, req.Target)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Message) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Sprintf("修改说明不能为空.\n修复操作: 执行 `pair stage note %s \"<说明>\"`", req.Target))
	}
	var text string
	if req.AssumptionId != "" {
		as := metaOf(e.Doc, s.ID).Assumptions
		i := slices.IndexFunc(as, func(a doc.Assumption) bool { return a.ID == req.AssumptionId })
		if i < 0 {
			return nil, connect.NewError(connect.CodeNotFound,
				fmt.Sprintf("区块「%s」中没有假设「%s」.\n修复操作: 执行 `pair show %[1]s` 查看该区块的 assumptions", req.Target, req.AssumptionId))
		}
		text = as[i].Text
	}
	fb, err := w.store.StageNote(branch, e.ID+"#"+s.ID, req.AssumptionId, text, req.Message)
	if err != nil {
		return nil, err
	}
	w.emit(&pairv1.Event{Type: evStageChanged, Branch: branch, Ids: []string{fb.Id}})
	return &pairv1.StageNoteResponse{Item: fb}, nil
}

// StageRemove 删除一条尚未发送的暂存条目.
func (w *Workspace) StageRemove(_ context.Context, req *pairv1.StageRemoveRequest) (*pairv1.StageRemoveResponse, error) {
	_, branch := w.snapshot()
	if err := w.store.RemoveStaged(branch, req.Id); err != nil {
		return nil, storeErr(err, fmt.Sprintf("暂存区中没有「%s」, 它可能已发送或不存在.\n修复操作: 执行 `pair status` 查看暂存条目", req.Id))
	}
	w.emit(&pairv1.Event{Type: evStageChanged, Branch: branch, Ids: []string{req.Id}})
	return &pairv1.StageRemoveResponse{}, nil
}

// ListFeedback 列出当前分支的反馈条目.
func (w *Workspace) ListFeedback(_ context.Context, req *pairv1.ListFeedbackRequest) (*pairv1.ListFeedbackResponse, error) {
	ix, branch := w.snapshot()
	items, err := w.store.ListFeedback(branch, req.States...)
	if err != nil {
		return nil, err
	}
	for _, f := range items {
		enrich(ix, f)
	}
	return &pairv1.ListFeedbackResponse{Items: items}, nil
}

// Trigger 发送暂存区的全部条目.
func (w *Workspace) Trigger(_ context.Context, req *pairv1.TriggerRequest) (*pairv1.TriggerResponse, error) {
	ix, branch := w.snapshot()
	items, err := w.store.Trigger(branch, req.Steer)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, f := range items {
		enrich(ix, f)
		ids = append(ids, f.Id)
	}
	if len(ids) > 0 {
		w.emit(&pairv1.Event{Type: evTrigger, Branch: branch, Ids: ids, Steer: req.Steer})
	}
	return &pairv1.TriggerResponse{Items: items}, nil
}

// enrich 补全存储中没有的展示字段: 问题所选方案的对比卡片标题. targets、progress 与
// assumption_text 已由存储记录.
func enrich(ix *repo.Index, f *pairv1.Feedback) *pairv1.Feedback {
	if f.Kind != kindDecision {
		return f
	}
	if e := ix.Decision(f.Id); e != nil && e.Doc != nil {
		if it := option(e.Doc, f.Pick); it != nil {
			f.PickTitle = it.Title
		}
	}
	return f
}
