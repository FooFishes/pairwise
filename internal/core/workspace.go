// Package core 实现常驻进程托管的业务操作, 直接实现生成的 PairServiceHandler.
package core

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"slices"
	"sync"

	"connectrpc.com/connect/v2"

	pairv1 "github.com/FooFishes/pairwise/gen/pair/v1"
	"github.com/FooFishes/pairwise/gen/pair/v1/pairv1connect"
	"github.com/FooFishes/pairwise/internal/check"
	"github.com/FooFishes/pairwise/internal/daemon"
	"github.com/FooFishes/pairwise/internal/doc"
	"github.com/FooFishes/pairwise/internal/repo"
	"github.com/FooFishes/pairwise/internal/store"
)

// subBuffer 是每个订阅者的事件缓冲; 缓冲写满说明订阅者跟不上, 关闭其通道让它凭 Last-Event-ID 重连.
const subBuffer = 64

// 事件类型, 见 proto 中 Event 的注释.
const (
	evDocChanged     = "doc_changed"
	evBranchChanged  = "branch_changed"
	evStageChanged   = "stage_changed"
	evTrigger        = "trigger"
	evDrained        = "drained"
	evResolved       = "resolved"
	evDecisionClosed = "decision_closed"
)

// Workspace 是一个工作树的业务状态: 文档索引、运行态存储与事件订阅.
type Workspace struct {
	pairv1connect.UnimplementedPairServiceHandler

	repo  *repo.Repo
	store *store.Store

	// mu 保护 ix、branch 与 uiURL; ix 是只读快照, 取出后无需持锁读取.
	mu     sync.Mutex
	ix     *repo.Index
	branch string
	uiURL  string

	// wmu 串行化写 .pair/ 文件的操作, 使读取磁盘、合并与写回之间不被其他写入穿插.
	wmu sync.Mutex

	// subMu 同时串行化事件持久化与扇出, 保证订阅者按序号递增收到事件.
	subMu sync.Mutex
	subs  map[chan *pairv1.Event]struct{}

	done     chan struct{}
	doneOnce sync.Once
}

var _ daemon.Service = (*Workspace)(nil)

// New 打开 dir 所在工作树, 供 daemon.Run 使用.
func New(ctx context.Context, dir string) (daemon.Service, error) {
	r, err := repo.Open(ctx, dir)
	if err != nil {
		return nil, err
	}
	branch, err := r.Git.Branch(ctx)
	if err != nil {
		return nil, err
	}
	ix, err := r.Load()
	if err != nil {
		return nil, err
	}
	st, err := store.Open(r.StateDir)
	if err != nil {
		return nil, err
	}
	return &Workspace{
		repo:   r,
		store:  st,
		ix:     ix,
		branch: branch,
		subs:   map[chan *pairv1.Event]struct{}{},
		done:   make(chan struct{}),
	}, nil
}

// snapshot 返回当前索引与分支.
func (w *Workspace) snapshot() (*repo.Index, string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ix, w.branch
}

// reload 从磁盘重建索引, 对内容哈希变化 (含新增与删除) 的文档发 doc_changed, 返回新索引.
func (w *Workspace) reload() (*repo.Index, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	ix, err := w.repo.Load()
	if err != nil {
		return nil, err
	}
	old := fingerprints(w.ix)
	var ids []string
	for k, h := range fingerprints(ix) {
		if old[k] != h {
			ids = append(ids, k.id)
		}
		delete(old, k)
	}
	for k := range old {
		ids = append(ids, k.id)
	}
	w.ix = ix
	if len(ids) > 0 {
		slices.Sort(ids)
		w.emit(&pairv1.Event{Type: evDocChanged, Branch: w.branch, Ids: slices.Compact(ids)})
	}
	return ix, nil
}

type entryKey struct {
	kind doc.Kind
	id   string
}

// fingerprints 返回每份文档的内容哈希; 无法解析的文档以错误信息代替.
func fingerprints(ix *repo.Index) map[entryKey]string {
	out := make(map[entryKey]string, len(ix.Entries))
	for _, e := range ix.Entries {
		h := "!" + errString(e.Err)
		if e.Doc != nil {
			h = doc.FileHash(e.Doc.Raw)
		}
		out[entryKey{e.Kind, e.ID}] = h
	}
	return out
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// emit 持久化事件后非阻塞地扇出给订阅者. 事件已是操作的附带通知, 持久化失败只记日志.
func (w *Workspace) emit(e *pairv1.Event) {
	w.subMu.Lock()
	defer w.subMu.Unlock()
	saved, err := w.store.AppendEvent(e)
	if err != nil {
		slog.Warn("记录事件失败", "type", e.Type, "err", err)
		return
	}
	for ch := range w.subs {
		select {
		case ch <- saved:
		default:
			delete(w.subs, ch)
			close(ch)
		}
	}
}

// Subscribe 订阅此后追加的事件.
func (w *Workspace) Subscribe() (<-chan *pairv1.Event, func()) {
	ch := make(chan *pairv1.Event, subBuffer)
	w.subMu.Lock()
	w.subs[ch] = struct{}{}
	w.subMu.Unlock()
	return ch, func() {
		w.subMu.Lock()
		defer w.subMu.Unlock()
		if _, ok := w.subs[ch]; ok {
			delete(w.subs, ch)
			close(ch)
		}
	}
}

// EventsSince 返回序号大于 seq 的已持久化事件.
func (w *Workspace) EventsSince(seq int64) ([]*pairv1.Event, error) {
	return w.store.EventsSince(seq, 0)
}

// Reload 重建文档索引.
func (w *Workspace) Reload(ctx context.Context) {
	if _, err := w.reload(); err != nil {
		slog.Warn("重建文档索引失败", "err", err)
	}
}

// BranchChanged 按新分支重建索引并使旧分支的绑定失效.
func (w *Workspace) BranchChanged(ctx context.Context) {
	branch, err := w.repo.Git.Branch(ctx)
	if err != nil {
		slog.Warn("读取当前分支失败", "err", err)
		return
	}
	w.mu.Lock()
	changed := branch != w.branch
	w.branch = branch
	w.mu.Unlock()
	if changed {
		if err := w.store.InvalidateBindings(branch); err != nil {
			slog.Warn("使旧分支绑定失效失败", "err", err)
		}
		w.emit(&pairv1.Event{Type: evBranchChanged, Branch: branch})
	}
	w.Reload(ctx)
}

// SetUIURL 记录界面地址.
func (w *Workspace) SetUIURL(url string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.uiURL = url
}

func (w *Workspace) uiURLValue() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.uiURL
}

// Done 在收到 Shutdown 请求后关闭.
func (w *Workspace) Done() <-chan struct{} {
	return w.done
}

// Close 释放存储.
func (w *Workspace) Close() error {
	return w.store.Close()
}

// Shutdown 通知常驻进程退出.
func (w *Workspace) Shutdown(context.Context, *pairv1.ShutdownRequest) (*pairv1.ShutdownResponse, error) {
	w.doneOnce.Do(func() { close(w.done) })
	return &pairv1.ShutdownResponse{}, nil
}

// Ping 返回进程与工作树信息, 供 CLI 校验构建指纹.
func (w *Workspace) Ping(context.Context, *pairv1.PingRequest) (*pairv1.PingResponse, error) {
	_, branch := w.snapshot()
	seq, err := w.store.LastSeq()
	if err != nil {
		return nil, err
	}
	return &pairv1.PingResponse{
		LastSeq:     seq,
		Version:     daemon.Version,
		Fingerprint: daemon.Fingerprint(),
		Root:        w.repo.Root(),
		Branch:      branch,
		Pid:         int32(os.Getpid()),
	}, nil
}

// Status 汇总文档、反馈队列与诊断.
func (w *Workspace) Status(context.Context, *pairv1.StatusRequest) (*pairv1.StatusResponse, error) {
	ix, branch := w.snapshot()
	resp := &pairv1.StatusResponse{Root: w.repo.Root(), Branch: branch, UiUrl: w.uiURLValue()}
	for _, e := range ix.Entries {
		resp.Docs = append(resp.Docs, summary(e))
	}
	items, err := w.store.ListFeedback(branch)
	if err != nil {
		return nil, err
	}
	for _, f := range items {
		switch f.State {
		case pairv1.FeedbackState_FEEDBACK_STATE_STAGED:
			resp.Staged++
		case pairv1.FeedbackState_FEEDBACK_STATE_TRIGGERED:
			resp.Triggered++
		case pairv1.FeedbackState_FEEDBACK_STATE_DRAINED:
			resp.Drained++
		}
	}
	for _, d := range check.Run(ix, check.Options{}) {
		if d.Severity == doc.SeverityError {
			resp.ErrorCount++
		} else {
			resp.WarningCount++
		}
	}
	return resp, nil
}

// touchTurn 在请求带会话时更新该会话当前回合的累计状态.
func (w *Workspace) touchTurn(sessionID string, f func(t *store.Turn)) error {
	if sessionID == "" {
		return nil
	}
	t, err := w.store.Turn(sessionID)
	if err != nil {
		return err
	}
	f(t)
	return w.store.SaveTurn(t)
}

// appendNew 把 ids 中尚未出现的项追加到 list.
func appendNew(list []string, ids ...string) []string {
	for _, id := range ids {
		if !slices.Contains(list, id) {
			list = append(list, id)
		}
	}
	return list
}

func isNotFound(err error) bool {
	return errors.Is(err, store.ErrNotFound)
}

// storeErr 把存储的哨兵错误转换为面向模型的错误.
func storeErr(err error, notFound string) error {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, notFound)
	case errors.Is(err, store.ErrSent):
		return connect.NewError(connect.CodeFailedPrecondition, err.Error()+"\n修复操作: 等待 Agent 执行 `pair drain` 并闭环后再重新选择")
	}
	return err
}
