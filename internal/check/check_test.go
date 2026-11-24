package check

import (
	"context"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/FooFishes/pairwise/internal/doc"
	"github.com/FooFishes/pairwise/internal/repo"
)

// newRepo 在临时目录中初始化分支为 main 的仓库, 写入 files 并隔离用户与系统级 git 配置.
func newRepo(t *testing.T, files map[string]string) (r *repo.Repo, git func(args ...string)) {
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
	git = func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	writeFiles(t, root, files)
	if r, err = repo.Open(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	return r, git
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

func load(t *testing.T, r *repo.Repo) *repo.Index {
	t.Helper()
	ix, err := r.Load()
	if err != nil {
		t.Fatal(err)
	}
	return ix
}

const (
	discover = `---
id: discover
scope:
  - "src/discover/**"
sections:
  list:
    assumptions:
      - id: shared-sort
        text: "共用排序状态"
---

# 发现页

## 列表 <!-- #list -->

<!-- pw:scenarios id="states" -->
#### 白天 <!-- #day -->
白天展示
<!-- /pw:scenarios -->
`
	pickShop = `---
id: pick-shop
scope:
  - "src/shop/**"
---

## 列表 <!-- #list -->

与 @discover#list 的白天场景 @discover#states/day 一致, 本页 @pick-shop#list.
`
	meal = `---
id: meal
---

## 步骤 <!-- #steps -->

先看 @discover#list, 再看 @pick-shop#list.
`
	closedShop = `---
id: closed-shop
targets:
  - discover#list
  - pick-shop#list
default_option: opt-a
---

# 歇业店铺如何展示

<!-- pw:compare id="options" -->
#### 方案 A <!-- #opt-a -->
发现页剔除, 选店保留
#### 方案 B <!-- #opt-b -->
全部保留
<!-- /pw:compare -->
`
)

func TestRun(t *testing.T) {
	base := map[string]string{
		".pair/modules/discover.md":      discover,
		".pair/modules/pick-shop.md":     pickShop,
		".pair/flows/meal.md":            meal,
		".pair/decisions/closed-shop.md": closedShop,
	}
	tests := []struct {
		name   string
		files  map[string]string
		strict bool
		want   []string
	}{
		{"全部合法", nil, false, nil},
		{"strict 报告未决问题", nil, true, []string{".pair/decisions/closed-shop.md:pending-decision"}},
		{"引用不可解析", map[string]string{
			".pair/flows/meal.md": "---\nid: meal\n---\n\n## 步骤 <!-- #steps -->\n" +
				"@nope#x @discover#missing @discover#states/night @discover#list/day @closed-shop#options\n",
		}, false, []string{
			".pair/flows/meal.md:unresolved-ref", ".pair/flows/meal.md:unresolved-ref", ".pair/flows/meal.md:unresolved-ref",
			".pair/flows/meal.md:unresolved-ref", ".pair/flows/meal.md:unresolved-ref",
		}},
		{"问题缺 default_option", map[string]string{
			".pair/decisions/closed-shop.md": strings.Replace(closedShop, "default_option: opt-a\n", "", 1),
		}, false, []string{".pair/decisions/closed-shop.md:missing-default-option"}},
		{"default_option 不是方案", map[string]string{
			".pair/decisions/closed-shop.md": strings.Replace(closedShop, "opt-a\n---", "opt-c\n---", 1),
		}, false, []string{".pair/decisions/closed-shop.md:invalid-default-option"}},
		{"问题缺对比容器且目标不可解析", map[string]string{
			".pair/decisions/closed-shop.md": "---\nid: closed-shop\ntargets: [\"discover#nope\", \"bad target\"]\ndefault_option: opt-a\n---\n\n正文\n",
		}, false, []string{
			".pair/decisions/closed-shop.md:missing-compare",
			".pair/decisions/closed-shop.md:unresolved-target",
			".pair/decisions/closed-shop.md:invalid-target",
		}},
		{"问题目标须是 ## 区块的规范路径", map[string]string{
			".pair/decisions/closed-shop.md": strings.Replace(closedShop, "  - pick-shop#list\n",
				"  - pick-shop\n  - \"@pick-shop#list\"\n  - discover#states\n  - discover#states/day\n", 1),
		}, false, []string{
			".pair/decisions/closed-shop.md:invalid-target", ".pair/decisions/closed-shop.md:invalid-target",
			".pair/decisions/closed-shop.md:invalid-target", ".pair/decisions/closed-shop.md:invalid-target",
		}},
		{"id 与文件名不一致", map[string]string{
			".pair/modules/discover.md": strings.Replace(discover, "id: discover", "id: discovery", 1),
		}, false, []string{".pair/modules/discover.md:id-mismatch"}},
		{"模块缺 scope", map[string]string{
			".pair/modules/pick-shop.md": strings.Replace(pickShop, "scope:\n  - \"src/shop/**\"\n", "", 1),
		}, false, []string{".pair/modules/pick-shop.md:missing-scope"}},
		{"元数据指向不存在的区块", map[string]string{
			".pair/modules/pick-shop.md": strings.Replace(pickShop, "---\n\n", "sections:\n  gone:\n    confirmed_hash: \"abc\"\n---\n\n", 1),
		}, false, []string{".pair/modules/pick-shop.md:stale-meta"}},
		{"假设 id 重复", map[string]string{
			".pair/modules/discover.md": strings.Replace(discover, "        text: \"共用排序状态\"\n",
				"        text: \"共用排序状态\"\n      - id: shared-sort\n        text: \"重复\"\n", 1),
		}, false, []string{".pair/modules/discover.md:duplicate-assumption"}},
		{"模块与流程 ID 冲突", map[string]string{
			".pair/flows/discover.md": "---\nid: discover\n---\n",
		}, false, []string{".pair/flows/discover.md:duplicate-doc-id"}},
		{"文件无法解析", map[string]string{
			".pair/flows/broken.md": "---\nid: [\n---\n",
		}, false, []string{".pair/flows/broken.md:parse-error"}},
		{"解析诊断原样汇总", map[string]string{
			".pair/flows/meal.md": meal + "\n## 无锚点\n",
		}, false, []string{".pair/flows/meal.md:missing-anchor"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := maps.Clone(base)
			maps.Copy(files, tt.files)
			r, _ := newRepo(t, files)
			ds := Run(load(t, r), Options{Strict: tt.strict})
			var got []string
			for _, d := range ds {
				got = append(got, d.Path+":"+d.Code)
				if d.Fix == "" || d.Line == 0 {
					t.Errorf("诊断缺少修复指引或行号: %+v", d)
				}
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("Run = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRunStrictPromotesWarnings(t *testing.T) {
	r, _ := newRepo(t, map[string]string{
		".pair/modules/pick-shop.md": strings.Replace(pickShop, "---\n\n", "sections:\n  gone:\n    confirmed_hash: \"abc\"\n---\n\n", 1),
		".pair/modules/discover.md":  discover,
	})
	ix := load(t, r)
	if ds := Run(ix, Options{}); len(ds) != 1 || ds[0].Severity != doc.SeverityWarning || ds[0].Line != 6 {
		t.Fatalf("Run = %+v, want 第 6 行的一条警告", ds)
	}
	if ds := Run(ix, Options{Strict: true}); len(ds) != 1 || ds[0].Severity != doc.SeverityError {
		t.Errorf("Strict 时警告应升级为错误: %+v", ds)
	}
}

func TestDiff(t *testing.T) {
	ctx := context.Background()
	r, git := newRepo(t, map[string]string{
		".pair/config.toml":          "ignore_globs = [\"src/discover/gen/**\"]\n",
		".pair/modules/discover.md":  discover,
		".pair/modules/pick-shop.md": pickShop,
		"src/discover/a.go":          "package discover",
		"src/shop/b.go":              "package shop",
	})
	git("add", ".")
	git("commit", "-q", "-m", "init")
	root := r.Root()

	// 暂存区模式: discover 源码有改动而文档未改, pick-shop 文档随源码一起改动, 忽略路径不计入.
	writeFiles(t, root, map[string]string{
		"src/discover/a.go":          "package discover // 改",
		"src/discover/gen/x.go":      "package gen",
		"src/shop/b.go":              "package shop // 改",
		".pair/modules/pick-shop.md": pickShop + "\n补充\n",
		"README.md":                  "说明",
	})
	git("add", ".")
	ix := load(t, r)
	res, err := Diff(ctx, r, ix, DiffOptions{Message: "调整发现页\n"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"README.md", "src/discover/a.go", "src/shop/b.go"}; !slices.Equal(res.Changed, want) {
		t.Errorf("Changed = %q, want %q", res.Changed, want)
	}
	if len(res.Missing) != 1 || res.Missing[0].DocID != "discover" || res.Missing[0].Path != ".pair/modules/discover.md" ||
		!slices.Equal(res.Missing[0].Files, []string{"src/discover/a.go"}) || res.OK() {
		t.Errorf("暂存区模式 = %+v, want discover 漏更新", res)
	}
	res, err = Diff(ctx, r, ix, DiffOptions{Message: "调整发现页\n\nPair-Skip: 只调样式\n"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.SkipReasons, []string{"只调样式"}) || !res.OK() {
		t.Errorf("Pair-Skip 应放行: %+v", res)
	}
	git("commit", "-q", "-m", "chore")

	// 区间模式: 分支上第一个提交漏改文档, 第二个提交带 Pair-Skip trailer.
	git("checkout", "-q", "-b", "feat")
	writeFiles(t, root, map[string]string{"src/discover/a.go": "package discover // 再改"})
	git("commit", "-q", "-am", "fix")
	res, err = Diff(ctx, r, ix, DiffOptions{BaseRef: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Missing) != 1 || res.Missing[0].DocID != "discover" || len(res.SkipReasons) != 0 || res.OK() {
		t.Errorf("区间模式 = %+v, want discover 漏更新", res)
	}
	writeFiles(t, root, map[string]string{"src/shop/b.go": "package shop // 再改"})
	git("commit", "-q", "-am", "refactor", "-m", "Pair-Skip: 内部重构")
	res, err = Diff(ctx, r, ix, DiffOptions{BaseRef: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Missing) != 2 || !slices.Equal(res.SkipReasons, []string{"内部重构"}) || !res.OK() {
		t.Errorf("区间内的 Pair-Skip 应放行: %+v", res)
	}
}
