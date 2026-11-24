package gitx

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// newRepo 在临时目录中初始化分支为 main 的仓库, 并隔离用户与系统级 git 配置.
func newRepo(t *testing.T) (root string, git func(args ...string) string) {
	t.Helper()
	for k, v := range map[string]string{
		"GIT_CONFIG_GLOBAL":   os.DevNull,
		"GIT_CONFIG_NOSYSTEM": "1",
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
	root = filepath.Join(root, "main")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	git = func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	return root, git
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func open(t *testing.T, dir string) *Repo {
	t.Helper()
	r, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestOpen(t *testing.T) {
	ctx := context.Background()
	root, git := newRepo(t)
	write(t, filepath.Join(root, "sub", "a.txt"), "a")
	git("add", ".")
	git("commit", "-q", "-m", "init")

	r := open(t, filepath.Join(root, "sub"))
	gitDir := filepath.Join(root, ".git")
	if want := (Repo{Root: root, GitDir: gitDir, CommonDir: gitDir}); *r != want {
		t.Errorf("主工作树 = %+v, want %+v", *r, want)
	}

	wt := filepath.Join(filepath.Dir(root), "wt")
	git("worktree", "add", "-q", "-b", "feat", wt)
	w := open(t, filepath.Join(wt, "sub"))
	if want := (Repo{Root: wt, GitDir: filepath.Join(gitDir, "worktrees", "wt"), CommonDir: gitDir}); *w != want {
		t.Errorf("链接工作树 = %+v, want %+v", *w, want)
	}
	if _, err := os.Stat(w.HeadPath()); err != nil {
		t.Errorf("HeadPath: %v", err)
	}
	if b, err := w.Branch(ctx); err != nil || b != "feat" {
		t.Errorf("Branch = %q, %v", b, err)
	}
	if h, err := w.HooksDir(ctx); err != nil || h != filepath.Join(gitDir, "hooks") {
		t.Errorf("HooksDir = %q, %v", h, err)
	}

	if err := w.SetConfig(ctx, "core.hooksPath", ".githooks"); err != nil {
		t.Fatal(err)
	}
	if h, err := w.HooksDir(ctx); err != nil || h != filepath.Join(wt, ".githooks") {
		t.Errorf("core.hooksPath 下 HooksDir = %q, %v", h, err)
	}
	if v := git("config", "--local", "core.hooksPath"); v != ".githooks" {
		t.Errorf("SetConfig 写入 %q", v)
	}

	if _, err := Open(ctx, t.TempDir()); err == nil || !strings.Contains(err.Error(), "不在 Git 工作树中") {
		t.Errorf("非仓库目录 err = %v", err)
	}
}

func TestBranch(t *testing.T) {
	ctx := context.Background()
	root, git := newRepo(t)
	r := open(t, root)
	branch := func() string {
		t.Helper()
		b, err := r.Branch(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	if b := branch(); b != "main" {
		t.Errorf("尚无提交 = %q", b)
	}
	git("commit", "-q", "--allow-empty", "-m", "init")
	if b := branch(); b != "main" {
		t.Errorf("有提交 = %q", b)
	}
	git("checkout", "-q", "--detach")
	if b, want := branch(), "HEAD@"+git("rev-parse", "--short", "HEAD"); b != want {
		t.Errorf("分离头指针 = %q, want %q", b, want)
	}
}

func TestChangedFiles(t *testing.T) {
	ctx := context.Background()
	root, git := newRepo(t)
	r := open(t, root)
	changed := func(cached bool, base string) []string {
		t.Helper()
		files, err := r.ChangedFiles(ctx, cached, base)
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(files)
		return files
	}

	odd := "目录/带 空格\t\"引号\".md"
	write(t, filepath.Join(root, odd), "x")
	write(t, filepath.Join(root, "old.go"), "package a\n")
	git("add", ".")
	if got, want := changed(true, ""), []string{"old.go", odd}; !slices.Equal(got, want) {
		t.Errorf("尚无提交时暂存区 = %q, want %q", got, want)
	}
	git("commit", "-q", "-m", "init")
	if got := changed(true, ""); len(got) != 0 {
		t.Errorf("暂存区为空 = %q", got)
	}

	git("checkout", "-q", "-b", "feat")
	git("mv", "old.go", "new.go")
	if got, want := changed(true, ""), []string{"new.go", "old.go"}; !slices.Equal(got, want) {
		t.Errorf("重命名暂存 = %q, want %q", got, want)
	}
	git("commit", "-q", "-m", "rename")
	// main 上的后续提交不应计入 main...HEAD.
	git("checkout", "-q", "main")
	write(t, filepath.Join(root, "main.go"), "package a\n")
	git("add", ".")
	git("commit", "-q", "-m", "main")
	git("checkout", "-q", "feat")
	if got, want := changed(false, "main"), []string{"new.go", "old.go"}; !slices.Equal(got, want) {
		t.Errorf("main...HEAD = %q, want %q", got, want)
	}
}

func TestHistory(t *testing.T) {
	ctx := context.Background()
	root, git := newRepo(t)
	r := open(t, root)
	if shas, err := r.FileHistory(ctx, "a.md", 10); err != nil || len(shas) != 0 {
		t.Errorf("尚无提交 FileHistory = %q, %v", shas, err)
	}

	commit := func(file, content string, msg ...string) string {
		t.Helper()
		write(t, filepath.Join(root, file), content)
		git("add", ".")
		args := []string{"commit", "-q"}
		for _, m := range msg {
			args = append(args, "-m", m)
		}
		git(args...)
		return git("rev-parse", "HEAD")
	}
	commit("a.md", "v1\r\n", "base")
	git("branch", "base")
	c2 := commit("a.md", "v2\n", "改 a", "正文", "Pair-Skip: 仅重构\nSigned-off-by: t <t@example.com>")
	commit("b.md", "b", "改 b")
	c4 := commit("a.md", "v3\n", "再改 a")

	shas, err := r.FileHistory(ctx, "a.md", 2)
	if err != nil || !slices.Equal(shas, []string{c4, c2}) {
		t.Errorf("FileHistory = %q, %v, want %q", shas, err, []string{c4, c2})
	}
	if b, err := r.ShowFile(ctx, "base", "a.md"); err != nil || string(b) != "v1\r\n" {
		t.Errorf("ShowFile = %q, %v", b, err)
	}

	msgs, err := r.CommitMessages(ctx, "base")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 || !strings.HasPrefix(msgs[0], "再改 a") {
		t.Fatalf("CommitMessages = %q", msgs)
	}
	var skips []string
	for _, m := range msgs {
		skips = append(skips, Trailers(m, "pair-skip")...)
	}
	if !slices.Equal(skips, []string{"仅重构"}) {
		t.Errorf("Pair-Skip = %q", skips)
	}
}

func TestTrailers(t *testing.T) {
	tests := []struct {
		name, msg string
		want      []string
	}{
		{"单个", "标题\n\nPair-Skip: 原因\n", []string{"原因"}},
		{"key 大小写不敏感且收集多个值", "标题\n\n正文\n\npair-skip: a\nSigned-off-by: t <t@x>\nPAIR-SKIP : b", []string{"a", "b"}},
		{"续行", "标题\n\nPair-Skip: 第一行\n  第二行", []string{"第一行 第二行"}},
		{"CRLF", "标题\r\n\r\nPair-Skip: a\r\n", []string{"a"}},
		{"只有标题", "Pair-Skip: a", nil},
		{"不在最后一段", "标题\n\nPair-Skip: a\n\n正文", nil},
		{"段内混有普通行", "标题\n\n正文\nPair-Skip: a", nil},
		{"key 含空格不是 trailer", "标题\n\nPair Skip: a", nil},
		{"空值", "标题\n\nPair-Skip:", nil},
		{"Git 生成的 trailer 放宽比例", "标题\n\nPair-Skip: a\n(cherry picked from commit abc)", []string{"a"}},
		{"忽略注释", "标题\n\nPair-Skip: a\n\n# 请输入提交说明\n#\n", []string{"a"}},
		{"剪切线之后不属于提交信息", "标题\n\nPair-Skip: a\n# ------------------------ >8 ------------------------\n# 不要修改\ndiff --git a/x b/x\n+k: v", []string{"a"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Trailers(tt.msg, "Pair-Skip"); !slices.Equal(got, tt.want) {
				t.Errorf("Trailers = %q, want %q", got, tt.want)
			}
		})
	}
}
