package core

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"

	pairv1 "github.com/FooFishes/pairwise/gen/pair/v1"
	"github.com/FooFishes/pairwise/internal/doc"
	"github.com/FooFishes/pairwise/internal/hook"
	"github.com/FooFishes/pairwise/internal/store"
)

// hook 以 Claude Code 的 Hook 输入格式调用 Hook, fields 是追加在 session_id 与 cwd 之后的 JSON 字段.
func (w *Workspace) hook(t *testing.T, event, session, fields string) string {
	t.Helper()
	payload := fmt.Sprintf(`{"session_id":%q,"cwd":%q,"hook_event_name":"x"%s}`, session, w.repo.Root(), fields)
	return string(must(w.Hook(ctx, &pairv1.HookRequest{Event: event, Payload: []byte(payload)})).Output)
}

// editTool 返回编辑工具写入工作树内 rel 的 Hook 字段.
func (w *Workspace) editTool(tool, rel string) string {
	return fmt.Sprintf(`,"tool_name":%q,"tool_input":{"file_path":%q}`, tool, filepath.Join(w.repo.Root(), rel))
}

func TestHookTurn(t *testing.T) {
	w := newWorkspace(t)
	start := must(w.Hook(ctx, &pairv1.HookRequest{Event: hook.SessionStart, EnvFile: "/tmp/env",
		Payload: []byte(`{"session_id":"s1","cwd":"/","hook_event_name":"SessionStart","source":"startup"}`)}))
	if start.EnvFileAppend != "export PAIR_SESSION_ID=s1\n" || len(start.Output) != 0 {
		t.Fatalf("session-start = %v", start)
	}
	if out := w.hook(t, hook.PromptSubmit, "s1", `,"prompt":"改排序"`); out != "" {
		t.Errorf("有环境文件且无待读反馈时 prompt-submit 输出 %s", out)
	}
	// 没有环境文件的会话 (路径 ③) 在每回合上下文中拿到短码.
	code := must(w.store.UpdateSession("s2", func(*store.Session) {})).Code
	if out := w.hook(t, hook.PromptSubmit, "s2", ""); !strings.Contains(out, `"hookEventName":"UserPromptSubmit"`) || !strings.Contains(out, "`--session "+code+"`") {
		t.Errorf("路径 ③ prompt-submit = %s", out)
	}

	if out := w.hook(t, hook.BeforeTool, "s1", w.editTool("Edit", ".pair/modules/discover.md")); !strings.Contains(out, `"permissionDecision":"deny"`) ||
		!strings.Contains(out, "`pair show discover`") || !strings.Contains(out, "`pair update discover#<sec> --base <hash> <<'EOF'`") {
		t.Errorf("编辑 .pair/ = %s", out)
	}
	src := w.editTool("Write", "lib/discover/page.go")
	if out := w.hook(t, hook.BeforeTool, "s1", src); !strings.Contains(out, "`pair bind discover`") || strings.Contains(out, "permissionDecision") {
		t.Errorf("scope 提示 = %s", out)
	}
	if out := w.hook(t, hook.BeforeTool, "s1", src); out != "" {
		t.Errorf("同一模块一回合提示了两次: %s", out)
	}
	w.hook(t, hook.AfterTool, "s1", src)
	w.hook(t, hook.AfterTool, "s1", w.editTool("Edit", "README.md"))
	if turn := must(w.store.Turn("s1")); !slices.Equal(turn.Modified, []string{"lib/discover/page.go", "README.md"}) {
		t.Errorf("modified = %v", turn.Modified)
	}

	// 同一原因只拦截一次, 第二次放行并记为待核对原因; 新回合清空.
	out := w.hook(t, hook.BeforeStop, "s1", `,"stop_hook_active":false`)
	if !strings.HasPrefix(out, `{"decision":"block","reason":"本回合修改了以下模块 scope 下的源码`) || !strings.Contains(out, "lib/discover/page.go") ||
		!strings.Contains(out, "`pair update discover#<sec> --base <hash>`") || !strings.Contains(out, `pair skip-update --reason \"<原因>\"`) {
		t.Fatalf("before-stop = %s", out)
	}
	if out := w.hook(t, hook.BeforeStop, "s1", `,"stop_hook_active":true`); out != "" {
		t.Fatalf("第二次结束未放行: %s", out)
	}
	if s := must(w.store.Session("s1")); s.State != "idle" || !strings.Contains(s.Unverified, "discover") {
		t.Errorf("放行后会话 = %+v", s)
	}
	w.hook(t, hook.PromptSubmit, "s1", "")
	if s := must(w.store.Session("s1")); s.State != "running" || s.Unverified != "" {
		t.Errorf("新回合会话 = %+v", s)
	}

	// skip-update 放行, 原因待写入提交信息.
	w.hook(t, hook.AfterTool, "s1", src)
	must(w.SkipUpdate(ctx, &pairv1.SkipUpdateRequest{SessionId: "s1", Reason: "只调样式"}))
	if out := w.hook(t, hook.BeforeStop, "s1", ""); out != "" {
		t.Errorf("skip-update 后 before-stop = %s", out)
	}
	if s := must(w.store.Session("s1")); s.Unverified != "" {
		t.Errorf("unverified = %q", s.Unverified)
	}
	if got := must(w.store.PendingSkips()); !slices.Equal(got, []string{"只调样式"}) {
		t.Errorf("skips = %v", got)
	}

	// 立即转向: 只放行 Bash 中的 pair 命令; 结束前要求读取, 读取后要求闭环.
	w.hook(t, hook.PromptSubmit, "s1", "")
	note := must(w.StageNote(ctx, &pairv1.StageNoteRequest{Target: "discover#tabs", Message: "各分类独立排序"})).Item
	must(w.Trigger(ctx, &pairv1.TriggerRequest{Steer: true}))
	if out := w.hook(t, hook.BeforeTool, "s1", `,"tool_name":"Read","tool_input":{"file_path":"/x"}`); !strings.Contains(out, "deny") || !strings.Contains(out, "`pair drain`") {
		t.Errorf("立即转向 = %s", out)
	}
	if out := w.hook(t, hook.BeforeTool, "s1", `,"tool_name":"Bash","tool_input":{"command":"pair drain"}`); out != "" {
		t.Errorf("pair 命令被拦截: %s", out)
	}
	if out := w.hook(t, hook.BeforeStop, "s1", ""); !strings.Contains(out, "尚未读取") || !strings.Contains(out, note.Id) {
		t.Errorf("未读反馈 before-stop = %s", out)
	}
	must(w.Drain(ctx, &pairv1.DrainRequest{SessionId: "s1"}))
	if out := w.hook(t, hook.BeforeStop, "s1", ""); !strings.Contains(out, "尚未闭环") || !strings.Contains(out, "剩余目标: discover#tabs") {
		t.Errorf("未闭环 before-stop = %s", out)
	}
	must(w.Update(ctx, &pairv1.UpdateRequest{Target: "discover#tabs", Base: w.showBase(t, "discover"), Content: "各分类独立排序.\n", Resolves: []string{note.Id}, SessionId: "s1"}))
	if out := w.hook(t, hook.BeforeStop, "s1", ""); out != "" {
		t.Errorf("闭环后 before-stop = %s", out)
	}
}

func TestBind(t *testing.T) {
	w := newWorkspace(t)
	for _, id := range []string{"s1", "s2", "s3"} {
		w.hook(t, hook.SessionStart, id, "")
	}
	code1 := must(w.store.Session("s1")).Code

	b1 := must(w.Bind(ctx, &pairv1.BindRequest{Targets: []string{"discover#tabs"}, SessionId: code1}))
	cur := doc.FileHash(w.read(t, doc.KindModule, "discover").Raw)
	if b1.BindId == "" || b1.SessionId != "s1" || len(b1.Docs) != 1 || b1.Docs[0].Base != cur || !slices.Equal(b1.Docs[0].Sections, []string{"tabs"}) {
		t.Fatalf("bind = %v", b1)
	}
	// 命中同一区块且双方都不带 --preserve: 拒绝; 区块不重叠或一方保持规格: 放行并附带提示.
	c := must(w.Bind(ctx, &pairv1.BindRequest{Targets: []string{"discover"}, SessionId: "s2"}))
	if c.BindId != "" || !slices.Equal(c.Conflicts, []string{"discover#tabs: 会话 " + code1 + " 正在修改"}) {
		t.Fatalf("冲突 = %v", c)
	}
	ok := must(w.Bind(ctx, &pairv1.BindRequest{Targets: []string{"discover#list"}, SessionId: "s2"}))
	pres := must(w.Bind(ctx, &pairv1.BindRequest{Targets: []string{"discover"}, SessionId: "s3", Preserve: "修复返回栈"}))
	if ok.BindId == "" || len(ok.Notices) != 1 || pres.BindId == "" || len(pres.Notices) != 2 {
		t.Fatalf("放行 = %v; %v", ok, pres)
	}
	sessions := must(w.ListSessions(ctx, &pairv1.ListSessionsRequest{})).Sessions
	i := slices.IndexFunc(sessions, func(s *pairv1.Session) bool { return s.Id == "s3" })
	if i < 0 || sessions[i].Preserve != "修复返回栈" || len(sessions[i].Bindings) != 1 {
		t.Errorf("sessions = %v", sessions)
	}

	// 保持规格的绑定: 改了 scope 下的源码也直接放行, 原因登记为跳过原因.
	w.hook(t, hook.AfterTool, "s3", w.editTool("Edit", "lib/discover/page.go"))
	if out := w.hook(t, hook.BeforeStop, "s3", ""); out != "" {
		t.Errorf("保持规格 before-stop = %s", out)
	}
	if got := must(w.store.PendingSkips()); !slices.Equal(got, []string{"修复返回栈"}) {
		t.Errorf("skips = %v", got)
	}

	// 省略 --base 时取绑定基准, 写入后推进, 连续更新不与自己上一次的改动冲突.
	for _, body := range []string{"分类正文 v2.\n", "分类正文 v3.\n"} {
		res := must(w.Update(ctx, &pairv1.UpdateRequest{Target: "discover#tabs", Content: body, SessionId: "s1"}))
		if bs := must(w.store.Bindings("s1")); bs[0].Base != res.Base {
			t.Fatalf("绑定基准 = %s, 新 base = %s", bs[0].Base, res.Base)
		}
	}

	// 路径 ④: 漏传 --session 的绑定由 after-tool 从 Bash 输出中关联.
	loose := must(w.Bind(ctx, &pairv1.BindRequest{Targets: []string{"discover#stage"}}))
	w.hook(t, hook.SessionStart, "s4", "")
	w.hook(t, hook.AfterTool, "s4", fmt.Sprintf(`,"tool_name":"Bash","tool_input":{"command":"pair bind discover#stage"},"tool_response":{"stdout":"bind_id: %s\n已绑定"}`, loose.BindId))
	if bs := must(w.store.Bindings("s4")); len(bs) != 1 || bs[0].ID != loose.BindId {
		t.Errorf("s4 bindings = %v", bs)
	}
}

// TestUpdateUnparseable 验证无法解析的已有文档只接受 --base 等于当前整文件哈希的整文档覆盖.
func TestUpdateUnparseable(t *testing.T) {
	w := newWorkspace(t)
	path := w.repo.Path(doc.KindModule, "discover")
	broken := []byte("---\nid: [discover\n---\n\n## 舞台 <!-- #stage -->\n")
	if err := os.WriteFile(path, broken, 0o644); err != nil {
		t.Fatal(err)
	}
	base := w.showBase(t, "discover")
	if base != doc.FileHash(broken) {
		t.Fatalf("show base = %s", base)
	}
	for _, req := range []*pairv1.UpdateRequest{
		{Target: "discover", Content: discover},
		{Target: "discover", Content: discover, Base: "000000000000"},
		{Target: "discover#stage", Content: "x\n", Base: base},
	} {
		if _, err := w.Update(ctx, req); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "`pair show discover`") {
			t.Errorf("Update(%s, base %q) err = %v", req.Target, req.Base, err)
		}
	}
	res := must(w.Update(ctx, &pairv1.UpdateRequest{Target: "discover", Content: discover, Base: base}))
	if !res.Changed || string(w.read(t, doc.KindModule, "discover").Raw) != discover {
		t.Errorf("覆盖结果 = %v", res)
	}
}
