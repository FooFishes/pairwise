package core

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"connectrpc.com/connect/v2"

	pairv1 "github.com/FooFishes/pairwise/gen/pair/v1"
	"github.com/FooFishes/pairwise/internal/doc"
	"github.com/FooFishes/pairwise/internal/repo"
	"github.com/FooFishes/pairwise/internal/store"
)

// sessionID 把 --session 的取值 (会话 ID 或短码) 规范为会话 ID, 找不到对应会话时原样使用.
func (w *Workspace) sessionID(idOrCode string) (string, error) {
	if idOrCode == "" {
		return "", nil
	}
	s, err := w.store.Session(idOrCode)
	if isNotFound(err) {
		return idOrCode, nil
	}
	if err != nil {
		return "", err
	}
	return s.ID, nil
}

// code 返回会话的短码, 会话未登记时返回会话 ID.
func (w *Workspace) code(id string) string {
	if s, err := w.store.Session(id); err == nil {
		return s.Code
	}
	return id
}

// Bind 为每个文档存下当前整文件 base 快照并记录绑定. 与其他会话的有效绑定命中同一区块且双方都不带 --preserve 时
// 不记录, 只在响应中列出冲突; 否则记录, 并在响应中附带其他会话正在修改的区块.
func (w *Workspace) Bind(ctx context.Context, req *pairv1.BindRequest) (*pairv1.BindResponse, error) {
	sid, err := w.sessionID(req.SessionId)
	if err != nil {
		return nil, err
	}
	w.bmu.Lock()
	defer w.bmu.Unlock()
	ix, branch, err := w.sync(ctx)
	if err != nil {
		return nil, err
	}
	if len(req.Targets) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, "缺少绑定目标.\n修复操作: 执行 `pair bind <module[#sec]>...`")
	}
	preserve := oneLine(req.Preserve)
	// 按文档归并目标, 同一文档只写文档 ID 的目标覆盖全部区块.
	var ts []doc.Target
	whole := map[string]bool{}
	for _, target := range req.Targets {
		t, err := parseTarget(target)
		if err != nil {
			return nil, err
		}
		ts = append(ts, t)
		whole[t.Doc] = whole[t.Doc] || t.Section == ""
	}
	resp := &pairv1.BindResponse{SessionId: sid, Preserve: preserve}
	var entries []*repo.Entry
	for _, t := range ts {
		var e *repo.Entry
		if t.Section == "" {
			e, err = findDoc(ix, t.Doc)
		} else {
			e, _, err = findSection(ix, t.String())
		}
		if err != nil {
			return nil, err
		}
		i := slices.Index(entries, e)
		if i < 0 {
			entries = append(entries, e)
			resp.Docs = append(resp.Docs, &pairv1.BoundDoc{DocId: e.ID})
			i = len(entries) - 1
		}
		if !whole[t.Doc] {
			resp.Docs[i].Sections = appendNew(resp.Docs[i].Sections, t.Section)
		}
	}

	others, err := w.store.ActiveBindings(branch)
	if err != nil {
		return nil, err
	}
	for _, d := range resp.Docs {
		for _, o := range others {
			if o.SessionID == sid || o.DocID != d.DocId {
				continue
			}
			shared, ok := overlap(d.Sections, o.Sections)
			if ok && preserve == "" && o.Preserve == "" {
				resp.Conflicts = append(resp.Conflicts, fmt.Sprintf("%s: 会话 %s 正在修改", where(d.DocId, shared), w.code(o.SessionID)))
				continue
			}
			notice := fmt.Sprintf("%s: 会话 %s 正在修改", where(o.DocID, o.Sections), w.code(o.SessionID))
			if o.Preserve != "" {
				notice += " (保持规格: " + o.Preserve + ")"
			}
			resp.Notices = append(resp.Notices, notice)
		}
	}
	if len(resp.Conflicts) > 0 {
		resp.Docs, resp.Notices = nil, nil
		return resp, nil
	}

	var bs []*store.Binding
	for i, d := range resp.Docs {
		if d.Base, err = w.store.PutBase(entries[i].Doc.Raw); err != nil {
			return nil, err
		}
		bs = append(bs, &store.Binding{SessionID: sid, Branch: branch, DocID: d.DocId, Sections: d.Sections, Base: d.Base, Preserve: preserve})
	}
	if resp.BindId, err = w.store.AddBindings(bs); err != nil {
		return nil, err
	}
	if sid != "" {
		w.emit(&pairv1.Event{Type: evSessionChanged, Branch: branch, SessionId: sid})
	}
	return resp, nil
}

// overlap 返回两组绑定区块的重叠部分, ok 报告是否重叠; 区块为空表示整个文档.
func overlap(a, b []string) (shared []string, ok bool) {
	switch {
	case len(a) == 0:
		return b, true
	case len(b) == 0:
		return a, true
	}
	for _, s := range a {
		if slices.Contains(b, s) {
			shared = append(shared, s)
		}
	}
	return shared, len(shared) > 0
}

// where 返回 "discover#tabs, discover#list" 或 "discover (全部区块)" 形式的绑定范围.
func where(docID string, secs []string) string {
	if len(secs) == 0 {
		return docID + " (全部区块)"
	}
	parts := make([]string, len(secs))
	for i, s := range secs {
		parts[i] = docID + "#" + s
	}
	return strings.Join(parts, ", ")
}

// oneLine 把原因压成一行: 它会作为 Pair-Skip trailer 写入提交信息.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// SkipUpdate 登记本回合不更新文档的原因: 记入待提交的跳过原因与会话的回合状态.
func (w *Workspace) SkipUpdate(_ context.Context, req *pairv1.SkipUpdateRequest) (*pairv1.SkipUpdateResponse, error) {
	reason := oneLine(req.Reason)
	if reason == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "跳过原因不能为空.\n修复操作: 执行 `pair skip-update --reason \"<不涉及规格变更的原因>\"`")
	}
	sid, err := w.sessionID(req.SessionId)
	if err != nil {
		return nil, err
	}
	if err := w.store.AddSkip(reason, sid); err != nil {
		return nil, err
	}
	return &pairv1.SkipUpdateResponse{Reason: reason}, w.touchTurn(sid, func(t *store.Turn) { t.Skipped = reason })
}

var sessionStates = []string{"idle", "running", "waiting"}

// SetSessionState 记录宿主上报的会话状态与能力, 会话未登记时以该 ID 登记.
func (w *Workspace) SetSessionState(_ context.Context, req *pairv1.SetSessionStateRequest) (*pairv1.SetSessionStateResponse, error) {
	switch {
	case !slices.Contains(sessionStates, req.State):
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Sprintf("会话状态 %q 不合法.\n修复操作: 改为 %s 之一", req.State, strings.Join(sessionStates, ", ")))
	case req.SessionId == "":
		return nil, connect.NewError(connect.CodeInvalidArgument,
			"缺少会话.\n修复操作: 带上 `--session <会话 ID 或短码>`, 或在设置了 PAIR_SESSION_ID 的会话中执行")
	}
	sid, err := w.sessionID(req.SessionId)
	if err != nil {
		return nil, err
	}
	_, err = w.store.UpdateSession(sid, func(s *store.Session) {
		s.State = req.State
		if req.Capabilities != nil {
			s.Capabilities = req.Capabilities
		}
	})
	if err != nil {
		return nil, err
	}
	_, branch := w.snapshot()
	w.emit(&pairv1.Event{Type: evSessionChanged, Branch: branch, SessionId: sid})
	return &pairv1.SetSessionStateResponse{}, nil
}

// ListSessions 列出最近活跃的会话及其有效绑定.
func (w *Workspace) ListSessions(ctx context.Context, _ *pairv1.ListSessionsRequest) (*pairv1.ListSessionsResponse, error) {
	if _, _, err := w.sync(ctx); err != nil {
		return nil, err
	}
	ss, err := w.store.Sessions()
	if err != nil {
		return nil, err
	}
	resp := &pairv1.ListSessionsResponse{}
	for _, s := range ss {
		bs, err := w.store.Bindings(s.ID)
		if err != nil {
			return nil, err
		}
		out := &pairv1.Session{Id: s.ID, ShortCode: s.Code, State: s.State, Capabilities: s.Capabilities, UpdatedAt: s.UpdatedAt, Unverified: s.Unverified}
		for _, b := range bs {
			out.Bindings = append(out.Bindings, &pairv1.BoundDoc{DocId: b.DocID, Sections: b.Sections, Base: b.Base})
			if b.Preserve != "" {
				out.Preserve = b.Preserve
			}
		}
		resp.Sessions = append(resp.Sessions, out)
	}
	return resp, nil
}
