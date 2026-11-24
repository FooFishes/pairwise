package repo

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/FooFishes/pairwise/internal/doc"
)

// newRepo 在临时目录中初始化 Git 仓库, 并隔离用户与系统级 git 配置.
func newRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", root, "init", "-q", "-b", "main").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	return root
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

func module(id string, scope ...string) string {
	s := "---\nid: " + id + "\nscope:\n"
	for _, g := range scope {
		s += "  - \"" + g + "\"\n"
	}
	return s + "---\n\n## 概述 <!-- #intro -->\n正文\n"
}

func TestOpen(t *testing.T) {
	ctx := context.Background()
	root := newRepo(t)
	write(t, filepath.Join(root, "src", "a.go"), "package a")

	r, err := Open(ctx, filepath.Join(root, "src"))
	if !errors.Is(err, ErrNotInitialized) || r == nil || r.Root() != root || r.PairDir != filepath.Join(root, Dir) {
		t.Fatalf("未初始化时 Open = %+v, %v", r, err)
	}

	write(t, filepath.Join(root, Dir, "config.toml"), "ignore_globs = [\"vendor/**\"]\n")
	r, err = Open(ctx, filepath.Join(root, "src"))
	if err != nil {
		t.Fatal(err)
	}
	if r.StateDir != filepath.Join(root, ".git", "pair") {
		t.Errorf("StateDir = %s", r.StateDir)
	}
	if !slices.Equal(r.Config.IgnoreGlobs, []string{"vendor/**"}) || r.Config.AskWaitTimeoutSecs != 90 {
		t.Errorf("Config = %+v, 缺省字段应取默认值", r.Config)
	}
	if _, err := os.Stat(r.StateDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Open 不应创建 StateDir")
	}
}

func TestLoadConfig(t *testing.T) {
	tests := []struct {
		name    string
		content string // 空串表示文件不存在
		want    Config
		wantErr bool
	}{
		{"文件不存在", "", DefaultConfig(), false},
		{"默认配置", string(DefaultConfigTOML()), Config{IgnoreGlobs: []string{}, AskWaitTimeoutSecs: 90}, false},
		{"部分字段", "ask_wait_timeout_secs = 30\n", Config{AskWaitTimeoutSecs: 30}, false},
		{"超时不是正数", "ask_wait_timeout_secs = 0\n", Config{}, true},
		{"非法 Glob", "ignore_globs = [\"a/[\"]\n", Config{}, true},
		{"非法 TOML", "ignore_globs = \n", Config{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if tt.content != "" {
				write(t, path, tt.content)
			}
			got, err := LoadConfig(path)
			if tt.wantErr {
				if err == nil {
					t.Errorf("LoadConfig = %+v, want error", got)
				}
				return
			}
			if err != nil || !slices.Equal(got.IgnoreGlobs, tt.want.IgnoreGlobs) || got.AskWaitTimeoutSecs != tt.want.AskWaitTimeoutSecs {
				t.Errorf("LoadConfig = %+v, %v, want %+v", got, err, tt.want)
			}
		})
	}
}

func TestLoad(t *testing.T) {
	root := newRepo(t)
	pair := filepath.Join(root, Dir)
	write(t, filepath.Join(pair, "config.toml"), "ignore_globs = [\"vendor/**\", \"**/*_test.go\"]\n")
	write(t, filepath.Join(pair, "modules", "b.md"), module("b", "src/**/*.go"))
	write(t, filepath.Join(pair, "modules", "a.md"), module("a", "src/a/**"))
	write(t, filepath.Join(pair, "modules", "a-b.md"), module("a-b", "lib/[")) // 非法 Glob 不命中任何路径
	write(t, filepath.Join(pair, "modules", "bad.md"), "---\nid: [\n---\n")
	write(t, filepath.Join(pair, "modules", "sub", "x.md"), module("x", "**"))
	write(t, filepath.Join(pair, "modules", "notes.txt"), "不是文档")
	write(t, filepath.Join(pair, "flows", "a.md"), "---\nid: a\n---\n")
	write(t, filepath.Join(pair, "decisions", "a.md"), "---\nid: a\n---\n")

	r, err := Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	ix, err := r.Load()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range ix.Entries {
		got = append(got, e.Kind.String()+":"+e.ID)
	}
	want := []string{"module:a", "module:a-b", "module:b", "module:bad", "flow:a", "decision:a"}
	if !slices.Equal(got, want) {
		t.Errorf("Entries = %q, want %q", got, want)
	}
	if e := ix.Doc("bad"); e == nil || e.Doc != nil || e.Err == nil || e.Rel != ".pair/modules/bad.md" {
		t.Errorf("无法解析的文档 = %+v", e)
	}
	if e := ix.Doc("a"); e.Kind != doc.KindModule || e.Path != filepath.Join(pair, "modules", "a.md") {
		t.Errorf("Doc(a) = %+v, 应优先返回模块", e)
	}
	if e := ix.Decision("a"); e == nil || e.Kind != doc.KindDecision {
		t.Errorf("Decision(a) = %+v", e)
	}
	if ix.Doc("x") != nil || len(ix.Of(doc.KindFlow)) != 1 {
		t.Errorf("子目录不应递归加载")
	}

	for rel, want := range map[string][]string{
		"src/a/x.go":   {"a", "b"},
		"src/a/x.ts":   {"a"},
		"src/c/d/e.go": {"b"},
		"README.md":    nil,
	} {
		var got []string
		for _, e := range ix.ModulesFor(rel) {
			got = append(got, e.ID)
		}
		if !slices.Equal(got, want) {
			t.Errorf("ModulesFor(%s) = %q, want %q", rel, got, want)
		}
	}

	for rel, want := range map[string]bool{
		".pair":               true,
		".pair/modules/a.md":  true,
		"vendor/x/y.go":       true,
		"src/a/x_test.go":     true,
		"src/a/x.go":          false,
		".pairs/x.go":         false,
		"internal/vendor/x.c": false,
	} {
		if got := r.Ignored(rel); got != want {
			t.Errorf("Ignored(%s) = %v, want %v", rel, got, want)
		}
	}
}

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.md")
	if err := WriteFileAtomic(path, []byte("一")); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o644 {
		t.Errorf("新文件权限 = %v, want 0644", fi.Mode().Perm())
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(path, []byte("二")); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(path)
	b, _ := os.ReadFile(path)
	if fi.Mode().Perm() != 0o600 || string(b) != "二" {
		t.Errorf("覆盖后 权限 = %v, 内容 = %q", fi.Mode().Perm(), b)
	}
	if des, _ := os.ReadDir(dir); len(des) != 1 {
		t.Errorf("目录中残留临时文件: %v", des)
	}
}
