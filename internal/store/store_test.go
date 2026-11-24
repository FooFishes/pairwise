package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	pairv1 "github.com/FooFishes/pairwise/gen/pair/v1"
)

func open(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func must[T any](t *testing.T) func(T, error) T {
	return func(v T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
}

func ids(fs []*pairv1.Feedback) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Id)
	}
	return out
}

func TestFeedbackLifecycle(t *testing.T) {
	s := open(t, t.TempDir())
	fb := must[*pairv1.Feedback](t)
	fbs := must[[]*pairv1.Feedback](t)

	d := fb(s.StageDecision("main", "shop-policy", "opt-a", "", []string{"discover#list", "pick#list"}))
	n := fb(s.StageNote("main", "discover#tabs", "a1", "默认按距离排序", "改成按评分"))
	if d.State != staged || d.CreatedAt == 0 || !slices.Equal(d.Targets, []string{"discover#list", "pick#list"}) {
		t.Fatalf("decision = %v", d)
	}
	if n.Id != "fb-2" || n.Kind != pairv1.FeedbackKind_FEEDBACK_KIND_NOTE || n.AssumptionText != "默认按距离排序" {
		t.Fatalf("note = %v", n)
	}
	if err := s.Resolve("main", "shop-policy"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("未发送的问题被闭环: %v", err)
	}
	if err := s.SetProgress("main", "shop-policy", "discover#list", "updated", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("未发送的问题记录了进度: %v", err)
	}

	got := fbs(s.Trigger("main", true))
	if !slices.Equal(ids(got), []string{"shop-policy", "fb-2"}) {
		t.Fatalf("trigger = %v", ids(got))
	}
	for _, f := range got {
		if f.State != triggered || !f.Steer || f.TriggeredAt == 0 {
			t.Fatalf("triggered = %v", f)
		}
	}
	if got := fbs(s.Trigger("main", false)); len(got) != 0 {
		t.Fatalf("重复 trigger = %v", ids(got))
	}

	got = fbs(s.Drain("main", "sess-1"))
	if !slices.Equal(ids(got), []string{"shop-policy", "fb-2"}) {
		t.Fatalf("drain = %v", ids(got))
	}
	for _, f := range got {
		if f.State != drained || f.DrainedBy != "sess-1" || f.DrainedAt == 0 {
			t.Fatalf("drained = %v", f)
		}
	}

	if err := s.SetProgress("main", "shop-policy", "discover#list", "updated", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetProgress("main", "shop-policy", "pick#list", "no_change", "规则不变"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetProgress("main", "shop-policy", "discover#list", "no_change", "改为无需改动"); err != nil {
		t.Fatal(err)
	}
	p := must[[]*pairv1.TargetProgress](t)(s.Progress("main", "shop-policy"))
	if len(p) != 2 || p[0].Target != "discover#list" || p[0].Status != "no_change" || p[1].Reason != "规则不变" {
		t.Fatalf("progress = %v", p)
	}

	for _, id := range []string{"shop-policy", "fb-2"} {
		if err := s.Resolve("main", id); err != nil {
			t.Fatal(err)
		}
	}
	if got := fbs(s.ListFeedback("main")); len(got) != 0 {
		t.Fatalf("闭环后仍有未闭环条目: %v", ids(got))
	}

	// 闭环后再次暂存同一问题产生新条目, 历史条目保留.
	again := fb(s.StageDecision("main", "shop-policy", "opt-b", "", nil))
	if again.State != staged || len(again.Progress) != 0 {
		t.Fatalf("again = %v", again)
	}
	if f := fb(s.Feedback("main", "shop-policy")); f.Pick != "opt-b" {
		t.Fatalf("最新条目 = %v", f)
	}
	hist := fbs(s.ListFeedback("main", resolved))
	if !slices.Equal(ids(hist), []string{"shop-policy", "fb-2"}) || hist[0].ResolvedAt == 0 || len(hist[0].Progress) != 2 {
		t.Fatalf("history = %v", hist)
	}
}

func TestBranchIsolation(t *testing.T) {
	s := open(t, t.TempDir())
	fb := must[*pairv1.Feedback](t)
	fbs := must[[]*pairv1.Feedback](t)

	fb(s.StageDecision("a", "q1", "opt-a", "", nil))
	fb(s.StageDecision("b", "q1", "opt-b", "", nil))
	fb(s.StageNote("b", "doc#sec", "", "", "补充说明"))

	if got := fbs(s.Trigger("a", false)); !slices.Equal(ids(got), []string{"q1"}) || got[0].Pick != "opt-a" {
		t.Fatalf("trigger a = %v", got)
	}
	if got := fbs(s.Drain("b", "sess")); len(got) != 0 {
		t.Fatalf("drain b = %v", ids(got))
	}
	if got := fbs(s.ListFeedback("b", staged)); len(got) != 2 {
		t.Fatalf("b staged = %v", ids(got))
	}
	if f := fb(s.Feedback("b", "q1")); f.State != staged || f.Pick != "opt-b" {
		t.Fatalf("b q1 = %v", f)
	}
	if err := s.RemoveStaged("a", "fb-3"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("跨分支删除: %v", err)
	}
}

func TestStageDecisionOverwrite(t *testing.T) {
	s := open(t, t.TempDir())
	fb := must[*pairv1.Feedback](t)

	first := fb(s.StageDecision("main", "q1", "opt-a", "先这样", []string{"x#a"}))
	second := fb(s.StageDecision("main", "q1", "opt-b", "改主意了", []string{"x#a", "y#b"}))
	if second.Pick != "opt-b" || second.Note != "改主意了" || len(second.Targets) != 2 || second.CreatedAt != first.CreatedAt {
		t.Fatalf("overwrite = %v", second)
	}
	if got := must[[]*pairv1.Feedback](t)(s.ListFeedback("main")); len(got) != 1 {
		t.Fatalf("覆盖后条目数 = %d", len(got))
	}

	must[[]*pairv1.Feedback](t)(s.Trigger("main", false))
	if _, err := s.StageDecision("main", "q1", "opt-c", "", nil); !errors.Is(err, ErrSent) {
		t.Fatalf("已发送时重复暂存: %v", err)
	}
	if err := s.RemoveStaged("main", "q1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删除已发送条目: %v", err)
	}
	if f := fb(s.Feedback("main", "q1")); f.Pick != "opt-b" || f.State != triggered {
		t.Fatalf("已发送条目被改动: %v", f)
	}
	if err := s.MarkDrained("main", "q1", "sess"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkDrained("main", "q1", "sess"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("重复标记已读取: %v", err)
	}

	n := fb(s.StageNote("main", "x#a", "", "", "删掉"))
	if err := s.RemoveStaged("main", n.Id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Feedback("main", n.Id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删除后仍存在: %v", err)
	}
	// 自增序号不复用, 新批注不会拿到被删除批注的 ID.
	if m := fb(s.StageNote("main", "x#a", "", "", "再加")); m.Id == n.Id {
		t.Fatalf("批注 ID 被复用: %s", m.Id)
	}
}

func TestEvents(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	ev := must[*pairv1.Event](t)
	evs := must[[]*pairv1.Event](t)

	for i, typ := range []string{"stage_changed", "trigger", "drained"} {
		e := ev(s.AppendEvent(&pairv1.Event{Type: typ, Branch: "main", Ids: []string{"q1"}, Steer: true}))
		if e.Seq != int64(i+1) || e.At == 0 {
			t.Fatalf("event %d = %v", i, e)
		}
	}

	tests := []struct {
		since int64
		limit int
		want  []int64
	}{
		{0, 0, []int64{1, 2, 3}},
		{1, 0, []int64{2, 3}},
		{1, 1, []int64{2}},
		{3, 10, nil},
	}
	for _, tt := range tests {
		var got []int64
		for _, e := range evs(s.EventsSince(tt.since, tt.limit)) {
			got = append(got, e.Seq)
		}
		if !slices.Equal(got, tt.want) {
			t.Errorf("EventsSince(%d, %d) = %v, want %v", tt.since, tt.limit, got, tt.want)
		}
	}
	if e := evs(s.EventsSince(1, 1))[0]; e.Type != "trigger" || e.Branch != "main" || !e.Steer || e.Ids[0] != "q1" || e.At == 0 {
		t.Fatalf("payload = %v", e)
	}

	// 重新打开后序号继续递增, 供 Last-Event-ID 跨重启补发.
	s.Close()
	s = open(t, dir)
	if e := ev(s.AppendEvent(&pairv1.Event{Type: "doc_changed"})); e.Seq != 4 {
		t.Fatalf("重启后 seq = %d", e.Seq)
	}
}

func TestBaseEviction(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	put := must[string](t)

	raw := []byte("# 文档\n")
	sum := sha256.Sum256(raw)
	if h := put(s.PutBase(raw)); h != hex.EncodeToString(sum[:])[:12] {
		t.Fatalf("hash = %s", h)
	}

	// 先写满 baseKeepCount+1 份, 把最早的 5 份改为 8 天前且依次更旧.
	var hashes []string
	for i := range baseKeepCount + 1 {
		hashes = append(hashes, put(s.PutBase(fmt.Appendf(nil, "base %d", i))))
	}
	for i, h := range hashes[:5] {
		old := time.Now().Add(-8*24*time.Hour - time.Duration(5-i)*time.Hour)
		if err := os.Chtimes(filepath.Join(dir, "bases", h), old, old); err != nil {
			t.Fatal(err)
		}
	}
	put(s.PutBase([]byte("newest")))

	// 共 103 份, 最近 100 份之外且超过 7 天的是最旧的 3 份.
	for i, h := range hashes {
		_, err := s.GetBase(h)
		if gone := errors.Is(err, ErrNotFound); gone != (i < 3) {
			t.Errorf("base %d: err = %v", i, err)
		}
	}

	if _, err := s.GetBase("../pair.db"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("非法哈希: %v", err)
	}
	if err := s.PutConfirmed("0123456789ab", []byte("## 标题")); err != nil {
		t.Fatal(err)
	}
	if got := must[[]byte](t)(s.GetConfirmed("0123456789ab")); string(got) != "## 标题" {
		t.Fatalf("confirmed = %q", got)
	}
	if _, err := s.GetConfirmed("ffffffffffff"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("confirmed missing: %v", err)
	}
}

func TestSessionsAndBindings(t *testing.T) {
	s := open(t, t.TempDir())
	sess := must[*Session](t)
	binds := must[[]*Binding](t)

	a := sess(s.UpdateSession("host-a", func(v *Session) { v.EnvFile = true }))
	if len(a.Code) != 4 || a.State != "idle" || !a.EnvFile || a.UpdatedAt == 0 {
		t.Fatalf("session = %+v", a)
	}
	a2 := sess(s.UpdateSession("host-a", func(v *Session) { v.State, v.Unverified = "running", "x" }))
	if a2.Code != a.Code || !a2.EnvFile || a2.State != "running" || a2.Unverified != "x" {
		t.Fatalf("update = %+v", a2)
	}
	if got := sess(s.Session(a.Code)); got.ID != "host-a" || got.Unverified != "x" {
		t.Fatalf("按短码查找 = %+v", got)
	}

	// 一次 bind 的多个文档共用 ID; 未关联会话的绑定不计入 ActiveBindings, 关联后同会话同文档以最新一次为准.
	old := must[string](t)(s.AddBindings([]*Binding{{SessionID: "host-a", Branch: "main", DocID: "discover", Base: "000000000000"}}))
	id := must[string](t)(s.AddBindings([]*Binding{
		{Branch: "main", DocID: "discover", Sections: []string{"tabs"}, Base: "0123456789ab"},
		{Branch: "main", DocID: "pick", Base: "0123456789ab"},
	}))
	if old != "b-1" || id != "b-2" {
		t.Fatalf("bind id = %s, %s", old, id)
	}
	if got := binds(s.ActiveBindings("main")); len(got) != 1 || got[0].ID != old {
		t.Fatalf("active = %+v", got)
	}
	if err := s.AttachBinding(id, "host-a"); err != nil {
		t.Fatal(err)
	}
	if err := s.AttachBinding(id, "host-b"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("改挂其他会话: %v", err)
	}
	got := binds(s.Bindings("host-a"))
	if len(got) != 2 || got[0].ID != id || got[0].Sections[0] != "tabs" || got[1].DocID != "pick" {
		t.Fatalf("bindings = %+v", got)
	}
	if ok := must[bool](t)(s.AdvanceBinding("host-a", "discover", "fedcba987654")); !ok {
		t.Fatal("未推进绑定基准")
	}
	if got := binds(s.Bindings("host-a")); got[0].Base != "fedcba987654" || got[1].Base != "0123456789ab" {
		t.Fatalf("推进后 bindings = %+v", got)
	}
	// 超过 24 小时没有活动的会话, 以及未登记会话的旧绑定, 都不再计入 ActiveBindings.
	must[string](t)(s.AddBindings([]*Binding{{SessionID: "manual", Branch: "main", DocID: "pick", Base: "0123456789ab"}}))
	if got := binds(s.ActiveBindings("main")); len(got) != 3 {
		t.Fatalf("active = %+v", got)
	}
	old24 := time.Now().Add(-25 * time.Hour).UnixMilli()
	if _, err := s.db.Exec(`UPDATE sessions SET updated_at = ?`, old24); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE bindings SET created_at = ? WHERE session_id = 'manual'`, old24); err != nil {
		t.Fatal(err)
	}
	if got := binds(s.ActiveBindings("main")); len(got) != 0 {
		t.Fatalf("不活跃会话的绑定仍计入: %+v", got)
	}
	if err := s.InvalidateBindings("feature"); err != nil {
		t.Fatal(err)
	}
	if got := binds(s.Bindings("host-a")); len(got) != 0 {
		t.Fatalf("切换分支后绑定未失效: %+v", got)
	}

	for _, r := range []string{"修复返回栈", "修复返回栈", "调样式"} {
		if err := s.AddSkip(r, "host-a"); err != nil {
			t.Fatal(err)
		}
	}
	if got := must[[]string](t)(s.PendingSkips()); !slices.Equal(got, []string{"修复返回栈", "调样式"}) {
		t.Fatalf("skips = %v", got)
	}
	// 只清除写入了提交信息的原因.
	if err := s.ConsumeSkips([]string{"修复返回栈", "其他"}); err != nil {
		t.Fatal(err)
	}
	if got := must[[]string](t)(s.PendingSkips()); !slices.Equal(got, []string{"调样式"}) {
		t.Fatalf("消费后 skips = %v", got)
	}
}

// TestUpdateTurn 验证并发的回合修改各自在单个事务内读改写, 互不覆盖.
func TestUpdateTurn(t *testing.T) {
	s := open(t, t.TempDir())
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			if _, err := s.UpdateTurn("host-a", func(tu *Turn) { tu.Modified = append(tu.Modified, fmt.Sprint(i)) }); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if got := must[*Turn](t)(s.Turn("host-a")); len(got.Modified) != 16 || got.SessionID != "host-a" {
		t.Fatalf("turn = %+v", got)
	}
	if err := s.ResetTurn("host-a"); err != nil {
		t.Fatal(err)
	}
	if got := must[*Turn](t)(s.Turn("host-a")); got.Modified != nil {
		t.Fatalf("reset turn = %+v", got)
	}
}

func TestConcurrentWrites(t *testing.T) {
	s := open(t, t.TempDir())
	var mode string
	if err := s.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q, %v", mode, err)
	}
	const workers, each = 8, 20
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for range workers {
		wg.Go(func() {
			for range each {
				if _, err := s.StageNote("main", "doc#sec", "", "", "并发"); err != nil {
					errs <- err
					return
				}
				if _, err := s.AppendEvent(&pairv1.Event{Type: "stage_changed"}); err != nil {
					errs <- err
					return
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if got := must[[]*pairv1.Feedback](t)(s.ListFeedback("main")); len(got) != workers*each {
		t.Fatalf("notes = %d", len(got))
	}
	if got := must[[]*pairv1.Event](t)(s.EventsSince(0, 0)); len(got) != workers*each || got[len(got)-1].Seq != workers*each {
		t.Fatalf("events = %d", len(got))
	}
}
