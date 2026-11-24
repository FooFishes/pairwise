package core

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"

	pairv1 "github.com/FooFishes/pairwise/gen/pair/v1"
	"github.com/FooFishes/pairwise/internal/doc"
)

const discover = `---
id: discover
scope:
  - "lib/discover/**"
sections:
  tabs:
    assumptions:
      - id: shared-sort
        text: "三个分类共用同一排序状态"
      - id: no-mealtime
        text: "非推荐排序不传饭点参数"
---

# 发现页

导语.

## 舞台 <!-- #stage -->

舞台正文.

## 分类栏 <!-- #tabs -->

分类正文.

## 列表 <!-- #list -->

列表正文.
`

const decision = `---
id: shop-policy
targets:
  - "discover#stage"
  - "discover#list"
default_option: opt-a
---

# 歇业门店处理

## 方案 <!-- #options -->

<!-- pw:compare id="cmp" -->

#### 发现页剔除 <!-- #opt-a -->

剔除.

#### 全部保留 <!-- #opt-b -->

保留.

<!-- /pw:compare -->
`

var ctx = context.Background()

// newWorkspace 在已提交 discover 模块的真实 Git 仓库中打开工作区, 并隔离用户与系统级 git 配置.
func newWorkspace(t *testing.T) *Workspace {
	t.Helper()
	for k, v := range map[string]string{
		"GIT_CONFIG_GLOBAL":   os.DevNull,
		"GIT_CONFIG_NOSYSTEM": "1",
		// 关闭提交后在后台运行的 git maintenance, 它会与临时目录的清理竞争.
		"GIT_CONFIG_COUNT":    "1",
		"GIT_CONFIG_KEY_0":    "maintenance.auto",
		"GIT_CONFIG_VALUE_0":  "false",
		"GIT_AUTHOR_NAME":     "t",
		"GIT_AUTHOR_EMAIL":    "t@example.com",
		"GIT_COMMITTER_NAME":  "t",
		"GIT_COMMITTER_EMAIL": "t@example.com",
	} {
		t.Setenv(k, v)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".pair", "modules", "discover.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(discover), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "."}, {"commit", "-q", "-m", "init"}} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	svc, err := New(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	w := svc.(*Workspace)
	t.Cleanup(func() { w.Close() })
	return w
}

// must 在出错时 panic, 测试随之失败并带调用栈.
func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func (w *Workspace) read(t *testing.T, kind doc.Kind, id string) *doc.Document {
	t.Helper()
	raw := must(os.ReadFile(w.repo.Path(kind, id)))
	return must(doc.Parse(raw, kind))
}

func (w *Workspace) showBase(t *testing.T, target string) string {
	t.Helper()
	return must(w.Show(ctx, &pairv1.ShowRequest{Target: target})).Base
}

func section(d *doc.Document, id string) string {
	return string(d.Text(d.Section(id).Span))
}

func ids(as []doc.Assumption) []string {
	var out []string
	for _, a := range as {
		out = append(out, a.ID)
	}
	return out
}

func TestUpdateSectionKeepsBytes(t *testing.T) {
	w := newWorkspace(t)
	before := w.read(t, doc.KindModule, "discover")
	resp := must(w.Update(ctx, &pairv1.UpdateRequest{Target: "discover#list", Base: w.showBase(t, "discover#list"), Content: "\n新的列表正文.\n"}))
	after := w.read(t, doc.KindModule, "discover")
	if !resp.Changed || resp.Base != doc.FileHash(after.Raw) {
		t.Fatalf("resp = %v", resp)
	}
	if string(after.Raw[:after.FrontEnd]) != string(before.Raw[:before.FrontEnd]) {
		t.Errorf("Frontmatter 字节被改动:\n%s", after.Raw[:after.FrontEnd])
	}
	for _, id := range []string{"stage", "tabs"} {
		if section(after, id) != section(before, id) {
			t.Errorf("未改动区块 %s 字节变化: %q", id, section(after, id))
		}
	}
	if got := section(after, "list"); got != "## 列表 <!-- #list -->\n\n新的列表正文.\n" {
		t.Errorf("list = %q", got)
	}
	if strings.Count(resp.Diff, "\n-") != 1 || !strings.Contains(resp.Diff, "-列表正文.") {
		t.Errorf("diff 应只删除被替换的行:\n%s", resp.Diff)
	}
}

func TestUpdateWithoutBase(t *testing.T) {
	w := newWorkspace(t)
	_, err := w.Update(ctx, &pairv1.UpdateRequest{Target: "discover#tabs", Content: "x\n"})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("err = %v", err)
	}
	msg := err.(*connect.Error).Message()
	for _, want := range []string{"拒绝更新: 模块 `discover` 已存在", "`--base`", "\n修复操作: ", "`pair show discover`", "`pair update discover#tabs --base <hash> <<'EOF'`"} {
		if !strings.Contains(msg, want) {
			t.Errorf("错误信息缺少 %q:\n%s", want, msg)
		}
	}
	if string(w.read(t, doc.KindModule, "discover").Raw) != discover {
		t.Error("被拒绝的更新写入了文件")
	}
}

func TestUpdateControlledFields(t *testing.T) {
	w := newWorkspace(t)
	must(w.Confirm(ctx, &pairv1.ConfirmRequest{Target: "discover#stage"}))
	cur := w.read(t, doc.KindModule, "discover")
	stageHash := cur.Meta("stage").ConfirmedHash
	// 输入伪造 confirmed_hash 与 retracted, 并不经 --resolves 删去 shared-sort.
	cur.Front.SetMeta("stage", &doc.SectionMeta{ConfirmedHash: "000000000000"})
	cur.Front.SetMeta("list", &doc.SectionMeta{Retracted: []doc.Assumption{{ID: "fake", Text: "伪造"}}})
	cur.Front.SetMeta("tabs", &doc.SectionMeta{Assumptions: []doc.Assumption{{ID: "no-mealtime", Text: "非推荐排序不传饭点参数"}}})
	resp := must(w.Update(ctx, &pairv1.UpdateRequest{Target: "discover", Base: w.showBase(t, "discover"), Content: string(render(cur))}))

	got := w.read(t, doc.KindModule, "discover")
	if h := got.Meta("stage").ConfirmedHash; h != stageHash {
		t.Errorf("confirmed_hash = %q, want %q", h, stageHash)
	}
	if got.Meta("list") != nil {
		t.Errorf("伪造的 retracted 被写入: %+v", got.Meta("list"))
	}
	tabs := got.Meta("tabs")
	if !slices.Equal(ids(tabs.Assumptions), []string{"no-mealtime"}) || !slices.Equal(ids(tabs.Retracted), []string{"shared-sort"}) {
		t.Errorf("tabs = %+v", tabs)
	}
	if len(resp.Retracted) != 1 || resp.Retracted[0].Id != "shared-sort" || len(resp.RemovedAssumptions) != 0 {
		t.Errorf("resp = %v", resp)
	}
}

func TestUpdateResolvesNote(t *testing.T) {
	w := newWorkspace(t)
	note := must(w.StageNote(ctx, &pairv1.StageNoteRequest{Target: "discover#tabs", AssumptionId: "shared-sort", Message: "各分类独立排序"})).Item
	req := &pairv1.UpdateRequest{Target: "discover#tabs", Base: w.showBase(t, "discover"), Content: "各分类独立排序.\n", Resolves: []string{note.Id}}
	if _, err := w.Update(ctx, req); err == nil || !strings.Contains(err.Error(), "开发者尚未确认") {
		t.Fatalf("未发送的批注被闭环: %v", err)
	}
	must(w.Trigger(ctx, &pairv1.TriggerRequest{}))
	drained := must(w.Drain(ctx, &pairv1.DrainRequest{})).Items
	if len(drained) != 1 || drained[0].AssumptionText != "三个分类共用同一排序状态" {
		t.Fatalf("drain = %v", drained)
	}
	resp := must(w.Update(ctx, req))

	tabs := w.read(t, doc.KindModule, "discover").Meta("tabs")
	if !slices.Equal(ids(tabs.Assumptions), []string{"no-mealtime"}) || len(tabs.Retracted) != 0 {
		t.Errorf("tabs = %+v", tabs)
	}
	if !slices.Equal(resp.RemovedAssumptions, []string{"shared-sort"}) || !slices.Equal(resp.Resolved, []string{note.Id}) || len(resp.Retracted) != 0 {
		t.Errorf("resp = %v", resp)
	}
	if fb := must(w.store.Feedback("main", note.Id)); fb.State != pairv1.FeedbackState_FEEDBACK_STATE_RESOLVED {
		t.Errorf("批注状态 = %v", fb.State)
	}
}

func TestDecisionClosure(t *testing.T) {
	w := newWorkspace(t)
	ask := must(w.Ask(ctx, &pairv1.AskRequest{Id: "shop-policy", Content: decision, SessionId: "s1"}))
	if ask.Replaced || len(ask.Decision.Options) != 2 || ask.Decision.DefaultOption != "opt-a" {
		t.Fatalf("ask = %v", ask)
	}
	must(w.StageDecide(ctx, &pairv1.StageDecideRequest{DecisionId: "shop-policy", Pick: "opt-b"}))

	waited := make(chan *pairv1.WaitDecisionResponse)
	go func() {
		r, _ := w.WaitDecision(ctx, &pairv1.WaitDecisionRequest{Id: "shop-policy", TimeoutSecs: 10, SessionId: "s1"})
		waited <- r
	}()
	time.Sleep(50 * time.Millisecond)
	must(w.Trigger(ctx, &pairv1.TriggerRequest{}))
	r := <-waited
	if !r.GetDecided() || r.Feedback.Pick != "opt-b" || r.Feedback.PickTitle != "全部保留" || r.Feedback.State != pairv1.FeedbackState_FEEDBACK_STATE_DRAINED {
		t.Fatalf("wait = %v", r)
	}

	resp := must(w.Update(ctx, &pairv1.UpdateRequest{Target: "discover#stage", Base: w.showBase(t, "discover"), Content: "保留歇业门店.\n",
		Resolves: []string{"shop-policy"}, SessionId: "s1"}))
	if len(resp.DeletedDecisions) != 0 || len(resp.OpenDecisions) != 1 || len(resp.OpenDecisions[0].Progress) != 1 {
		t.Fatalf("首个目标更新后 resp = %v", resp)
	}
	if _, err := w.Update(ctx, &pairv1.UpdateRequest{Target: "discover#stage", Resolves: []string{"shop-policy"}, NoChange: "x"}); err == nil || !strings.Contains(err.Error(), "已经更新过") {
		t.Fatalf("已更新的目标再标记无需改动 err = %v", err)
	}
	resp = must(w.Update(ctx, &pairv1.UpdateRequest{Target: "discover#list", Resolves: []string{"shop-policy"}, NoChange: "列表规则不变", SessionId: "s2"}))
	if !slices.Equal(resp.DeletedDecisions, []string{"shop-policy"}) || !slices.Equal(resp.Resolved, []string{"shop-policy"}) || len(resp.OpenDecisions) != 0 {
		t.Fatalf("全部目标完成后 resp = %v", resp)
	}
	if _, err := os.Stat(w.repo.Path(doc.KindDecision, "shop-policy")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("问题文件未删除: %v", err)
	}
	fb := must(w.store.Feedback("main", "shop-policy"))
	if fb.State != pairv1.FeedbackState_FEEDBACK_STATE_RESOLVED || fb.Progress[1].Status != progressNoChange || fb.Progress[1].Reason != "列表规则不变" {
		t.Errorf("feedback = %v", fb)
	}
	turn := must(w.store.Turn("s1"))
	if !turn.Asked || !slices.Equal(turn.Drained, []string{"shop-policy"}) || !slices.Equal(turn.Updated, []string{"discover"}) {
		t.Errorf("turn = %+v", turn)
	}
	// --no-change 不写文档, 不算本回合更新过该文档.
	if turn := must(w.store.Turn("s2")); len(turn.Updated) != 0 {
		t.Errorf("--no-change 后 turn = %+v", turn)
	}

	// 以同一 ID 重新提交的是新一轮问题: 不带上一轮的进度, 闭环前须等开发者确认.
	again := must(w.Ask(ctx, &pairv1.AskRequest{Id: "shop-policy", Content: decision}))
	if len(again.Decision.Progress) != 0 {
		t.Errorf("新一轮问题的进度 = %v", again.Decision.Progress)
	}
	if _, err := w.Update(ctx, &pairv1.UpdateRequest{Target: "discover#list", Resolves: []string{"shop-policy"}, NoChange: "x"}); err == nil || !strings.Contains(err.Error(), "开发者尚未确认") {
		t.Errorf("新一轮问题闭环 err = %v", err)
	}
	r = must(w.WaitDecision(ctx, &pairv1.WaitDecisionRequest{Id: "shop-policy", TimeoutSecs: 1}))
	if r.Decided || r.DefaultOption != "opt-a" {
		t.Errorf("超时 wait = %v", r)
	}
	// 已被 drain 读走的选择同样立即返回.
	must(w.StageDecide(ctx, &pairv1.StageDecideRequest{DecisionId: "shop-policy", Pick: "opt-b"}))
	must(w.Trigger(ctx, &pairv1.TriggerRequest{}))
	must(w.Drain(ctx, &pairv1.DrainRequest{}))
	if r = must(w.WaitDecision(ctx, &pairv1.WaitDecisionRequest{Id: "shop-policy", TimeoutSecs: 5})); !r.Decided || r.Feedback.Pick != "opt-b" {
		t.Errorf("drain 之后 wait = %v", r)
	}
}

func TestConfirmThenChanged(t *testing.T) {
	w := newWorkspace(t)
	before := w.read(t, doc.KindModule, "discover")
	conf := must(w.Confirm(ctx, &pairv1.ConfirmRequest{Target: "discover#tabs"}))
	after := w.read(t, doc.KindModule, "discover")
	if string(after.Raw[after.FrontEnd:]) != string(before.Raw[before.FrontEnd:]) || conf.Base != doc.FileHash(after.Raw) {
		t.Fatal("Confirm 改动了正文字节")
	}
	if m := after.Meta("tabs"); m.ConfirmedHash != after.Section("tabs").Hash || len(m.Assumptions) != 0 {
		t.Fatalf("tabs = %+v", m)
	}
	must(w.Update(ctx, &pairv1.UpdateRequest{Target: "discover#tabs", Base: conf.Base, Content: "分类正文, 修正错别字.\n"}))

	d := must(w.GetDoc(ctx, &pairv1.GetDocRequest{Id: "discover"})).Doc
	tabs := d.Sections[slices.IndexFunc(d.Sections, func(s *pairv1.Section) bool { return s.Id == "tabs" })]
	if tabs.Status != pairv1.SectionStatus_SECTION_STATUS_CHANGED || tabs.ConfirmedHash != after.Meta("tabs").ConfirmedHash {
		t.Errorf("tabs = %v", tabs)
	}
	diff := must(w.SectionDiff(ctx, &pairv1.SectionDiffRequest{Target: "discover#tabs"}))
	if !diff.Available || diff.Confirmed != "## 分类栏 <!-- #tabs -->\n\n分类正文." || !strings.Contains(diff.Current, "修正错别字") {
		t.Errorf("diff = %v", diff)
	}
}
