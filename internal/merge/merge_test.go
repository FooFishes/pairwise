package merge

import (
	"slices"
	"strings"
	"testing"

	"github.com/FooFishes/pairwise/internal/doc"
)

var labels = Labels{Base: "base", Ours: "ours", Theirs: "theirs"}

func parse(t *testing.T, s string) *doc.Document {
	t.Helper()
	if s == "" {
		return nil
	}
	d, err := doc.Parse([]byte(s), doc.KindModule)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func merge3(t *testing.T, base, ours, theirs string) *Result {
	t.Helper()
	r, err := Merge3(parse(t, base), parse(t, ours), parse(t, theirs), labels)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func wheres(r *Result) []string {
	var w []string
	for _, c := range r.Conflicts {
		w = append(w, c.Where)
	}
	return w
}

// baseDoc 的 Frontmatter 故意带注释与非常规引号, 用于验证未改动时字节原样保留.
const baseDoc = `---
id: discover   # 模块
scope: ['lib/**']
sections:
  stage:
    confirmed_hash: "a84f21c09e12"
  tabs:
    confirmed_hash: '3b91e04d77a1'
---

# 发现页

## 舞台 <!-- #stage -->
舞台正文

## 分类栏 <!-- #tabs -->
分类正文

## 列表 <!-- #list -->
列表正文
`

func TestMergeClean(t *testing.T) {
	ours := strings.Replace(baseDoc, "舞台正文", "舞台正文 ours 改", 1)
	theirs := strings.Replace(baseDoc, "列表正文", "列表正文 theirs 改", 1)
	tests := []struct {
		name               string
		base, ours, theirs string
		want               string
	}{
		{"三方相同", baseDoc, baseDoc, baseDoc, baseDoc},
		{"只有 ours 改", baseDoc, ours, baseDoc, ours},
		{"只有 theirs 改", baseDoc, baseDoc, theirs, theirs},
		{"不同区块各改", baseDoc, ours, theirs, strings.Replace(ours, "列表正文", "列表正文 theirs 改", 1)},
		{"只改空白不算改动", baseDoc, ours, strings.Replace(baseDoc, "分类正文\n", "分类正文   \n", 1), ours},
		{"theirs 删除未改区块", baseDoc, ours, strings.Replace(baseDoc, "## 分类栏 <!-- #tabs -->\n分类正文\n\n", "", 1),
			strings.Replace(ours, "## 分类栏 <!-- #tabs -->\n分类正文\n\n", "", 1)},
		{"theirs 改标题文字", baseDoc, ours, strings.Replace(baseDoc, "## 列表 <!-- #list -->", "## 推荐列表 <!-- #list -->", 1),
			strings.Replace(ours, "## 列表 <!-- #list -->", "## 推荐列表 <!-- #list -->", 1)},
		{"theirs Frontmatter 仅风格与注释不同", baseDoc, ours, strings.Replace(baseDoc, "id: discover   # 模块\nscope: ['lib/**']", "scope:\n  - lib/**\n# 注释\nid: \"discover\"", 1), ours},
		{"缺锚点按标题对齐", "## 甲\n一\n\n## 乙\n二\n", "## 甲\n一改\n\n## 乙\n二\n", "## 甲\n一\n\n## 乙\n二改\n", "## 甲\n一改\n\n## 乙\n二改\n"},
		{"前言区作为一个块", baseDoc, strings.Replace(baseDoc, "# 发现页", "# 发现页 v2", 1), theirs,
			strings.Replace(theirs, "# 发现页", "# 发现页 v2", 1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := merge3(t, tt.base, tt.ours, tt.theirs)
			if len(r.Conflicts) != 0 {
				t.Fatalf("conflicts = %q", wheres(r))
			}
			if string(r.Raw) != tt.want {
				t.Errorf("raw =\n%s\nwant\n%s\ndiff:\n%s", r.Raw, tt.want, Diff("want", "got", []byte(tt.want), r.Raw))
			}
		})
	}
}

func TestMergeInsertOrder(t *testing.T) {
	base := "# T\n\n## a <!-- #a -->\nA\n\n## b <!-- #b -->\nB\n"
	ours := "# T\n\n## a <!-- #a -->\nA\n\n## x <!-- #x -->\nX\n\n## b <!-- #b -->\nB\n"
	theirs := "# T\n\n## n0 <!-- #n0 -->\nN0\n\n## a <!-- #a -->\nA\n\n## b <!-- #b -->\nB\n\n## n1 <!-- #n1 -->\nN1\n\n## n2 <!-- #n2 -->\nN2\n"
	r := merge3(t, base, ours, theirs)
	want := "# T\n\n## n0 <!-- #n0 -->\nN0\n\n## a <!-- #a -->\nA\n\n## x <!-- #x -->\nX\n\n## b <!-- #b -->\nB\n\n## n1 <!-- #n1 -->\nN1\n\n## n2 <!-- #n2 -->\nN2\n"
	if len(r.Conflicts) != 0 || string(r.Raw) != want {
		t.Errorf("raw =\n%s conflicts=%q", r.Raw, wheres(r))
	}
	// theirs 新增区块的前驱被 ours 删除时, 落在更早的前驱之后.
	ours = "# T\n\n## b <!-- #b -->\nB\n"
	theirs = "# T\n\n## a <!-- #a -->\nA\n\n## n <!-- #n -->\nN\n\n## b <!-- #b -->\nB\n"
	if r := merge3(t, base, ours, theirs); string(r.Raw) != "# T\n\n## n <!-- #n -->\nN\n\n## b <!-- #b -->\nB\n" {
		t.Errorf("raw =\n%s", r.Raw)
	}
}

func TestMergeConflicts(t *testing.T) {
	tests := []struct {
		name               string
		base, ours, theirs string
		where              []string
		raw                string
	}{
		{"同区块两侧改", "## a <!-- #a -->\nA\n\n## b <!-- #b -->\nB\n", "## a <!-- #a -->\nA1\n\n## b <!-- #b -->\nB\n", "## a <!-- #a -->\nA2\n\n## b <!-- #b -->\nB2\n",
			[]string{"section:a"}, "<<<<<<< ours\n## a <!-- #a -->\nA1\n=======\n## a <!-- #a -->\nA2\n>>>>>>> theirs\n\n## b <!-- #b -->\nB2\n"},
		{"一侧删除一侧修改", "# T\n\n## a <!-- #a -->\nA\n", "# T\n", "# T\n\n## a <!-- #a -->\nA2\n",
			[]string{"section:a"}, "# T\n\n<<<<<<< ours\n=======\n## a <!-- #a -->\nA2\n>>>>>>> theirs\n"},
		{"双方新增同 ID 内容不同", "## a <!-- #a -->\nA\n", "## a <!-- #a -->\nA\n\n## n <!-- #n -->\nN1\n", "## a <!-- #a -->\nA\n\n## n <!-- #n -->\nN2\n",
			[]string{"section:n"}, "## a <!-- #a -->\nA\n\n<<<<<<< ours\n## n <!-- #n -->\nN1\n=======\n## n <!-- #n -->\nN2\n>>>>>>> theirs\n"},
		{"Frontmatter 同键冲突", "---\nid: a\nscope: [x]\n---\n## a <!-- #a -->\n", "---\nid: b\nscope: [x]\n---\n## a <!-- #a -->\n", "---\nid: c\nscope: [x]\n---\n## a <!-- #a -->\n",
			[]string{"frontmatter:id"}, "---\n<<<<<<< ours\nid: b\nscope: [x]\n=======\nid: c\nscope: [x]\n>>>>>>> theirs\n---\n## a <!-- #a -->\n"},
		{"各自新建前言区不同", "", "# A\n", "# B\n", []string{"preamble"}, "<<<<<<< ours\n# A\n=======\n# B\n>>>>>>> theirs\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := merge3(t, tt.base, tt.ours, tt.theirs)
			if got := wheres(r); !slices.Equal(got, tt.where) {
				t.Errorf("where = %q, want %q", got, tt.where)
			}
			if string(r.Raw) != tt.raw {
				t.Errorf("raw =\n%s\nwant\n%s", r.Raw, tt.raw)
			}
		})
	}
}

func TestMergeFrontAdjacent(t *testing.T) {
	ours := strings.Replace(baseDoc, "confirmed_hash: \"a84f21c09e12\"", "confirmed_hash: \"000000000001\"", 1)
	theirs := strings.Replace(baseDoc, "    confirmed_hash: '3b91e04d77a1'\n", "    confirmed_hash: '3b91e04d77a1'\n    assumptions:\n      - id: shared-sort\n        text: 共用排序\n", 1)
	r := merge3(t, baseDoc, ours, theirs)
	if len(r.Conflicts) != 0 {
		t.Fatalf("conflicts = %q", wheres(r))
	}
	want := strings.Replace(ours, "    confirmed_hash: '3b91e04d77a1'\n", "    confirmed_hash: '3b91e04d77a1'\n    assumptions:\n      - id: shared-sort\n        text: 共用排序\n", 1)
	// Frontmatter 有改动时重新编码, 注释与引号风格保留, 注释前的对齐空白由 yaml 编码器统一为一个空格.
	want = strings.Replace(want, "discover   # 模块", "discover # 模块", 1)
	if string(r.Raw) != want {
		t.Errorf("raw =\n%s\nwant\n%s", r.Raw, want)
	}
}

func TestMergeAssumptionsByID(t *testing.T) {
	front := func(items string) string {
		return "---\nid: m\nsections:\n  tabs:\n    assumptions:\n" + items + "---\n## t <!-- #tabs -->\n"
	}
	item := func(id, text string) string { return "      - id: " + id + "\n        text: " + text + "\n" }
	base := front(item("a", "甲") + item("b", "乙") + item("c", "丙"))
	ours := front(item("a", "甲改") + item("b", "乙") + item("o", "新o") + item("c", "丙"))
	theirs := front(item("a", "甲") + item("t", "新t") + item("c", "丙改"))
	r := merge3(t, base, ours, theirs)
	if len(r.Conflicts) != 0 {
		t.Fatalf("conflicts = %q", wheres(r))
	}
	d := parse(t, string(r.Raw))
	var got []string
	for _, a := range d.Meta("tabs").Assumptions {
		got = append(got, a.ID+"="+a.Text)
	}
	if want := []string{"a=甲改", "t=新t", "o=新o", "c=丙改"}; !slices.Equal(got, want) {
		t.Errorf("assumptions = %q, want %q", got, want)
	}
}

func TestMergeAssumptionConflict(t *testing.T) {
	front := func(text string) string {
		return "---\nsections:\n  tabs:\n    assumptions:\n      - id: a\n        text: " + text + "\n---\n"
	}
	r := merge3(t, front("甲"), front("甲1"), front("甲2"))
	if got := wheres(r); !slices.Equal(got, []string{"frontmatter:sections.tabs.assumptions.a.text"}) {
		t.Errorf("where = %q", got)
	}
}

func TestMergeDuplicateSection(t *testing.T) {
	d := parse(t, "## a <!-- #a -->\n## b <!-- #a -->\n")
	if _, err := Merge3(nil, d, d, labels); err == nil {
		t.Error("重复区块应返回错误")
	}
}

func TestMergeDoesNotMutate(t *testing.T) {
	ours := parse(t, "---\nid: m\nsections:\n  a:\n    confirmed_hash: \"1\"\n---\n")
	theirs := parse(t, "---\nid: m\nsections:\n  a:\n    confirmed_hash: \"1\"\n  b:\n    confirmed_hash: \"2\"\n---\n")
	base := parse(t, "---\nid: m\n---\n")
	before := string(ours.Front.Encode())
	if _, err := Merge3(base, ours, theirs, labels); err != nil {
		t.Fatal(err)
	}
	if string(ours.Front.Encode()) != before {
		t.Error("ours 的节点树被修改")
	}
}

func TestDiff(t *testing.T) {
	if Diff("a", "b", []byte("x\n"), []byte("x\n")) != "" {
		t.Error("无差异应为空")
	}
	got := Diff("a", "b", []byte("x\ny\n"), []byte("x\nz\n"))
	if !strings.Contains(got, "--- a\n+++ b\n") || !strings.Contains(got, "-y\n+z\n") {
		t.Errorf("diff = %q", got)
	}
}

func TestMergeIdentityKeepsBytes(t *testing.T) {
	for _, raw := range []string{
		"---\nid: a   # 注释\n---\n# T\n## a <!-- #a -->\nA\n## b <!-- #b -->\nB\n\n\n",
		"## a <!-- #a -->\r\nA\r\n\r\n## b <!-- #b -->\r\nB",
	} {
		if r := merge3(t, raw, raw, raw); string(r.Raw) != raw || len(r.Conflicts) != 0 {
			t.Errorf("Merge3(x, x, x) = %q, want %q", r.Raw, raw)
		}
	}
}
