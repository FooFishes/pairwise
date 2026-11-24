package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/FooFishes/pairwise/internal/check"
	"github.com/FooFishes/pairwise/internal/gitx"
)

// newRepo 在临时目录中初始化仓库并切换到其根目录, 写入 files 并隔离用户与系统级 git 配置.
func newRepo(t *testing.T, files map[string]string) (root string, git func(args ...string) string) {
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
	git = func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	git("init", "-q", "-b", "main")
	writeFiles(t, root, files)
	t.Chdir(root)
	return root, git
}

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// pair 在进程内执行一次命令, 返回退出码与 stdout、stderr.
func pair(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(context.Background(), nil, args, strings.NewReader(""), &out, &errOut)
	return code, out.String(), errOut.String()
}

// TestInitIdempotent 覆盖重复执行 init: 已有配置不覆盖, 规则不重复追加, 非 Pairwise 钩子不覆盖.
func TestInitIdempotent(t *testing.T) {
	custom := "#!/bin/sh\necho custom\n"
	root, git := newRepo(t, map[string]string{".git/hooks/prepare-commit-msg": custom})
	code, out, errOut := pair(t, "init", "--git-hooks")
	if code != 0 {
		t.Fatalf("init 退出码 %d: %s", code, errOut)
	}
	for _, want := range []string{"已创建 .pair/config.toml", "未覆盖已存在的非 Pairwise 钩子", "已安装 Git 钩子 commit-msg", "全仓迁移任务"} {
		if !strings.Contains(out, want) {
			t.Errorf("输出缺少 %q:\n%s", want, out)
		}
	}
	for _, d := range []string{"modules", "flows", "decisions"} {
		if fi, err := os.Stat(filepath.Join(root, ".pair", d)); err != nil || !fi.IsDir() {
			t.Errorf(".pair/%s 不是目录: %v", d, err)
		}
	}
	if got := strings.TrimSpace(git("config", "merge.pair.driver")); got != "pair merge-driver %O %A %B" {
		t.Errorf("merge.pair.driver = %q", got)
	}
	if got := readFile(t, filepath.Join(root, ".git/hooks/commit-msg")); !strings.Contains(got, `exec pair git-hook commit-msg "$@"`) {
		t.Errorf("commit-msg 钩子 = %q", got)
	}

	cfg := filepath.Join(root, ".pair/config.toml")
	writeFiles(t, root, map[string]string{".pair/config.toml": "ask_wait_timeout_secs = 30\n"})
	if code, out, errOut = pair(t, "init", "--git-hooks"); code != 0 {
		t.Fatalf("再次 init 退出码 %d: %s", code, errOut)
	}
	if !strings.Contains(out, "保留已有的 .pair/config.toml") {
		t.Errorf("再次 init 输出:\n%s", out)
	}
	if got := readFile(t, cfg); got != "ask_wait_timeout_secs = 30\n" {
		t.Errorf("config.toml 被覆盖: %q", got)
	}
	if got := readFile(t, filepath.Join(root, ".pair/.gitattributes")); got != mergeAttr+"\n" {
		t.Errorf(".gitattributes = %q", got)
	}
	if got := readFile(t, filepath.Join(root, ".git/hooks/prepare-commit-msg")); got != custom {
		t.Errorf("非 Pairwise 钩子被覆盖: %q", got)
	}
}

const mergeBase = `---
id: discover
scope: ["src/**"]
---

## 舞台 <!-- #stage -->
转盘

## 列表 <!-- #list -->
按距离排序
`

func TestMergeDriver(t *testing.T) {
	tests := []struct {
		name         string
		ours, theirs string
		code         int
		want         []string
	}{
		{
			name:   "不同区块自动合并",
			ours:   strings.Replace(mergeBase, "转盘", "转盘动画", 1),
			theirs: strings.Replace(mergeBase, "按距离排序", "按评分排序", 1),
			want:   []string{"转盘动画", "按评分排序"},
		},
		{
			name:   "同一区块冲突",
			ours:   strings.Replace(mergeBase, "按距离排序", "按销量排序", 1),
			theirs: strings.Replace(mergeBase, "按距离排序", "按评分排序", 1),
			code:   1,
			want:   []string{"<<<<<<< ours", "按销量排序", "=======", "按评分排序", ">>>>>>> theirs", "转盘"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			paths := make([]string, 3)
			for i, s := range []string{mergeBase, tt.ours, tt.theirs} {
				paths[i] = filepath.Join(dir, []string{"base", "ours", "theirs"}[i])
				if err := os.WriteFile(paths[i], []byte(s), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			code, _, errOut := pair(t, append([]string{"merge-driver"}, paths...)...)
			if code != tt.code {
				t.Fatalf("退出码 %d, 期望 %d: %s", code, tt.code, errOut)
			}
			got := readFile(t, paths[1])
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("合并结果缺少 %q:\n%s", w, got)
				}
			}
		})
	}
}

// TestCommitMsgMissing 覆盖 commit-msg: 源码命中 scope 而模块文档未改且无 Pair-Skip 时拒绝提交并给出修复命令.
func TestCommitMsgMissing(t *testing.T) {
	root, git := newRepo(t, map[string]string{".pair/modules/discover.md": mergeBase})
	git("add", ".")
	git("commit", "-q", "-m", "init")
	writeFiles(t, root, map[string]string{"src/a.go": "package a\n", "COMMIT_EDITMSG": "feat: a\n"})
	git("add", "src/a.go")
	code, _, errOut := pair(t, "git-hook", "commit-msg", "COMMIT_EDITMSG")
	if code != 1 {
		t.Fatalf("退出码 %d, 期望 1: %s", code, errOut)
	}
	for _, want := range []string{"提交被拒绝", "模块「discover」", "src/a.go", "pair show discover", "Pair-Skip: <原因>"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("输出缺少 %q:\n%s", want, errOut)
		}
	}
}

// TestAppendSkips 覆盖 prepare-commit-msg 追加 trailer: 相同 trailer 不重复, 编辑器模式下填写标题后 trailer 仍有效.
func TestAppendSkips(t *testing.T) {
	root, _ := newRepo(t, nil)
	g, err := gitx.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		msg    string
		editor bool
	}{
		{"命令行提交信息", "feat: x\n", false},
		{"已有相同 trailer", "feat: x\n\nPair-Skip: a\n", false},
		{"编辑器模板", "\n# Please enter the commit message\n", true},
		{"带签名的编辑器模板", "\n\nSigned-off-by: t <t@example.com>\n\n# Please enter the commit message\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := os.WriteFile("MSG", []byte(tt.msg), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := appendSkips(context.Background(), g, "MSG", []string{"a", "b c"}, tt.editor); err != nil {
				t.Fatal(err)
			}
			got := readFile(t, "MSG")
			if tt.editor {
				// 模拟开发者在编辑器首行填写标题.
				got = "feat: x" + got
			}
			if ts := gitx.Trailers(got, check.SkipTrailer); !slices.Equal(ts, []string{"a", "b c"}) {
				t.Errorf("trailer = %q, 提交信息:\n%s", ts, got)
			}
		})
	}
}

// TestExitCodes 覆盖不经常驻进程就能判定的退出码: 用法错误 2, 其余 0 或 1.
func TestExitCodes(t *testing.T) {
	tests := []struct {
		args []string
		code int
	}{
		{[]string{"--version"}, 0},
		{[]string{"nope"}, 2},
		{[]string{"list", "bogus"}, 2},
		{[]string{"stage"}, 2},
		{[]string{"stage", "bogus"}, 2},
		{[]string{"stage", "decide", "x"}, 2},
		{[]string{"serve", "--open", "--stop"}, 2},
		{[]string{"check", "main"}, 2},
		{[]string{"ask", "x", "--timeout", "5"}, 2},
		{[]string{"update", "x", "--kind", "decision"}, 2},
		{[]string{"update", "x", "--no-change", "r", "--file", "a.md"}, 2},
		{[]string{"watch"}, 2},
		// 宿主把退出码 2 视为阻塞, hook 的参数问题只以 1 结束.
		{[]string{"hook"}, 1},
		{[]string{"hook", "bogus", "--bogus"}, 1},
		{[]string{"hook", "before-tool", "--session"}, 1},
	}
	for _, tt := range tests {
		if code, out, errOut := pair(t, tt.args...); code != tt.code {
			t.Errorf("pair %v 退出码 %d, 期望 %d\n%s%s", tt.args, code, tt.code, out, errOut)
		}
	}
}

// TestHookWorktree 覆盖 hook 的失败处理: 未初始化的工作树静默放行, 已初始化时无法解析的输入以 1 结束.
func TestHookWorktree(t *testing.T) {
	root, _ := newRepo(t, nil)
	if code, out, errOut := pair(t, "hook", "before-tool"); code != 0 || out+errOut != "" {
		t.Errorf("未初始化: 退出码 %d\n%s%s", code, out, errOut)
	}
	writeFiles(t, root, map[string]string{".pair/config.toml": ""})
	if code, _, errOut := pair(t, "hook", "before-tool"); code != 1 || !strings.Contains(errOut, "宿主 Hook 输入不是合法的 JSON") {
		t.Errorf("无法解析的输入: 退出码 %d\n%s", code, errOut)
	}
}
