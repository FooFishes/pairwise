package doc

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

const exampleFront = `---
id: discover
scope:
  - "lib/features/discover/**"
  - "lib/data/recommend/**"
sections:
  stage:
    confirmed_hash: "a84f21c09e12"
  tabs:
    confirmed_hash: "3b91e04d77a1"
    assumptions:
      - id: shared-sort
        text: "三个分类共用同一排序状态，切换分类不重置"
      - id: no-mealtime
        text: "按非推荐排序请求列表时不传饭点过滤参数"
    retracted:
      - id: auto-reset-tab
        text: "切走页面再返回时自动重置回第一个分类"
---
`

func mustParse(t *testing.T, s string) *Document {
	t.Helper()
	d, err := Parse([]byte(s), KindModule)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func sectionIDs(d *Document) []string {
	var ids []string
	for _, s := range d.Sections {
		ids = append(ids, s.ID+"|"+s.Title)
	}
	return ids
}

func diagCodes(d *Document) []string {
	var codes []string
	for _, g := range d.Diags {
		codes = append(codes, g.Code)
	}
	return codes
}

func TestSections(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		want  []string
		diags []string
	}{
		{"普通区块", "# 标题\n\n## 甲 <!-- #a -->\n正文\n\n## 乙 <!-- #b -->\n", []string{"a|甲", "b|乙"}, nil},
		{"代码块中的标题", "## 甲 <!-- #a -->\n```\n## 不是 <!-- #x -->\n```\n~~~md\n## 也不是\n~~~\n", []string{"a|甲"}, nil},
		{"HTML 注释中的标题", "## 甲 <!-- #a -->\n<!--\n## 不是 <!-- #x -->\n-->\n", []string{"a|甲"}, nil},
		{"引用块与列表中的标题", "## 甲 <!-- #a -->\n> ## 引用 <!-- #q -->\n\n- ## 列表 <!-- #l -->\n", []string{"a|甲"}, nil},
		{"Setext 标题不是边界", "## 甲 <!-- #a -->\n乙\n---\n", []string{"a|甲"}, nil},
		{"缩进的 ATX 标题", "   ## 甲 <!-- #a -->\n", []string{"a|甲"}, nil},
		{"缺锚点仍是边界", "## 甲 <!-- #a -->\n## 乙\n正文\n", []string{"a|甲", "|乙"}, []string{"missing-anchor"}},
		{"非法锚点", "## 甲 <!-- #a.b -->\n", []string{"|甲 <!-- #a.b -->"}, []string{"missing-anchor"}},
		{"区块 ID 重复", "## 甲 <!-- #a -->\n## 乙 <!-- #a -->\n", []string{"a|甲", "a|乙"}, []string{"duplicate-id"}},
		{"链接引用定义", "[r]: https://e.com\n\n## 甲 <!-- #a -->\n\n[s]: https://e.com\n\n## 乙 <!-- #b -->\n", []string{"a|甲", "b|乙"}, nil},
		{"三级标题属于区块", "## 甲 <!-- #a -->\n### 子标题 <!-- #s -->\n", []string{"a|甲"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := mustParse(t, tt.in)
			if got := sectionIDs(d); !slices.Equal(got, tt.want) {
				t.Errorf("sections = %q, want %q", got, tt.want)
			}
			if got := diagCodes(d); !slices.Equal(got, tt.diags) {
				t.Errorf("diags = %q, want %q", got, tt.diags)
			}
		})
	}
}

func TestDocumentLayout(t *testing.T) {
	raw := exampleFront + "\n# 发现页\n\n导语\n\n## 舞台与转盘 <!-- #stage -->\n舞台\n\n## 分类栏与排序 <!-- #tabs -->\n### 浮层交互\n内容\n"
	d := mustParse(t, raw)
	if d.Title != "发现页" || d.Front.ID != "discover" {
		t.Fatalf("title=%q id=%q", d.Title, d.Front.ID)
	}
	if got := string(d.Text(d.Preamble)); got != "\n# 发现页\n\n导语\n\n" {
		t.Errorf("preamble = %q", got)
	}
	tabs := d.Section("tabs")
	if got := string(d.Text(tabs.Span)); got != "## 分类栏与排序 <!-- #tabs -->\n### 浮层交互\n内容\n" {
		t.Errorf("tabs span = %q", got)
	}
	if got := string(d.Text(tabs.Body)); got != "### 浮层交互\n内容\n" {
		t.Errorf("tabs body = %q", got)
	}
	if tabs.Line != 28 {
		t.Errorf("tabs line = %d", tabs.Line)
	}
	if !slices.Equal(d.Front.SectionOrder, []string{"stage", "tabs"}) || len(d.Meta("tabs").Assumptions) != 2 {
		t.Errorf("front = %+v", d.Front)
	}
	if d.Status(tabs) != StatusChanged {
		t.Errorf("status = %v", d.Status(tabs))
	}
	d.Front.Sections["tabs"].ConfirmedHash = tabs.Hash
	if d.Status(tabs) != StatusConfirmed || d.Status(&Section{ID: "none"}) != StatusUnconfirmed {
		t.Error("status after confirm")
	}
	if got := Assemble(d.Raw[:d.FrontEnd], d.Text(d.Preamble), d.Text(d.Section("stage").Span), d.Text(tabs.Span)); string(got) != raw {
		t.Errorf("assemble = %q", got)
	}
}

func TestTitleChangeKeepsAnchor(t *testing.T) {
	a := mustParse(t, "## 旧标题 <!-- #tabs -->\n正文\n")
	b := mustParse(t, "## 新标题 <!-- #tabs -->\n正文\n")
	if a.Sections[0].ID != b.Sections[0].ID {
		t.Fatal("anchor changed")
	}
	if a.Sections[0].Hash == b.Sections[0].Hash {
		t.Error("标题文字属于区块内容, 哈希应变化")
	}
}

func TestNormalize(t *testing.T) {
	tests := []struct{ in, want string }{
		{"a\r\nb\r\n", "a\nb"},
		{"a  \t\nb ", "a\nb"},
		{"\n\n  \na\n\n\n", "a"},
		{"  - a\n    - b\n\tcode", "  - a\n    - b\n\tcode"},
		{"a\n\n\nb", "a\n\n\nb"},
		{"   缩进首行\n", "   缩进首行"},
	}
	for _, tt := range tests {
		if got := string(Normalize([]byte(tt.in))); got != tt.want {
			t.Errorf("Normalize(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	if Hash([]byte("## a\r\nb  \n\n")) != Hash([]byte("## a\nb")) || Hash([]byte("  a")) == Hash([]byte("a")) {
		t.Error("hash normalization")
	}
	if len(FileHash([]byte("x"))) != 12 {
		t.Error("hash length")
	}
}

const containerDoc = `## 对比 <!-- #policy -->
导语

<!-- pw:compare id="closed" -->
#### 方案 A <!-- #opt-a -->
剔除

` + "```mermaid\nflowchart LR\n  A --> B\n```" + `

#### 方案 B <!-- #opt-b -->
保留
<!-- /pw:compare -->

` + "```pw-layout\nPage\n├── Header\n│   ├── Title\n│   └── Back\n└── Body\n    - Card\n      - Image\n```" + `

<!-- pw:workaround id="temp" -->
> **过渡做法（解除条件：推荐接口 v2 上线 (含排序)）**：前端本地排序
<!-- /pw:workaround -->
尾注
`

func TestBlocks(t *testing.T) {
	d := mustParse(t, containerDoc)
	if len(d.Diags) != 0 {
		t.Fatalf("diags = %+v", d.Diags)
	}
	s := d.Section("policy")
	var kinds []BlockKind
	for _, b := range s.Blocks {
		kinds = append(kinds, b.Kind)
	}
	want := []BlockKind{BlockMarkdown, BlockCompare, BlockLayout, BlockWorkaround, BlockMarkdown}
	if !slices.Equal(kinds, want) {
		t.Fatalf("kinds = %v, want %v", kinds, want)
	}
	if got := string(d.Text(s.Blocks[0].Span)); got != "导语\n" {
		t.Errorf("markdown = %q", got)
	}
	cmp := d.Container("closed")
	if !strings.HasPrefix(string(d.Text(cmp.Span)), "<!-- pw:compare") || !strings.HasSuffix(string(d.Text(cmp.Span)), "<!-- /pw:compare -->\n") {
		t.Errorf("compare span = %q", d.Text(cmp.Span))
	}
	if len(cmp.Items) != 2 || cmp.Items[0].ID != "opt-a" || cmp.Items[1].Title != "方案 B" {
		t.Fatalf("items = %+v", cmp.Items)
	}
	a := d.Item("closed", "opt-a")
	if len(a.Blocks) != 2 || a.Blocks[1].Kind != BlockMermaid || a.Blocks[1].Code != "flowchart LR\n  A --> B\n" {
		t.Errorf("item blocks = %+v", a.Blocks)
	}
	if got := string(d.Text(d.Item("closed", "opt-b").Body)); got != "保留\n" {
		t.Errorf("item body = %q", got)
	}
	if got := d.Container("temp").Condition; got != "推荐接口 v2 上线 (含排序)" {
		t.Errorf("condition = %q", got)
	}
	layout := s.Blocks[2].Layout
	type flat struct {
		label string
		depth int
	}
	var got []flat
	var walk func([]*LayoutNode, int)
	walk = func(ns []*LayoutNode, depth int) {
		for _, n := range ns {
			got = append(got, flat{n.Label, depth})
			walk(n.Children, depth+1)
		}
	}
	walk(layout, 0)
	wantLayout := []flat{{"Page", 0}, {"Header", 1}, {"Title", 2}, {"Back", 2}, {"Body", 1}, {"Card", 2}, {"Image", 3}}
	if !reflect.DeepEqual(got, wantLayout) {
		t.Errorf("layout = %v", got)
	}
}

func TestLayoutList(t *testing.T) {
	got := parseLayout("- 页面\n  - 头部\n  * 列表\n    - 卡片\n- 弹层\n")
	if len(got) != 2 || len(got[0].Children) != 2 || got[0].Children[1].Children[0].Label != "卡片" || got[1].Label != "弹层" {
		t.Errorf("layout = %+v", got)
	}
}

func TestContainerDiags(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		diags []string
		lines []int
	}{
		{"未闭合", "## a <!-- #a -->\n<!-- pw:compare id=\"c\" -->\n#### x <!-- #x -->\n## b <!-- #b -->\n", []string{"unclosed-container"}, []int{2}},
		{"嵌套", "## a <!-- #a -->\n<!-- pw:compare id=\"c\" -->\n<!-- pw:scenarios id=\"s\" -->\n<!-- /pw:compare -->\n", []string{"nested-container"}, []int{3}},
		{"错配", "## a <!-- #a -->\n<!-- pw:compare id=\"c\" -->\n<!-- /pw:scenarios -->\n", []string{"mismatched-container", "unclosed-container"}, []int{3, 2}},
		{"多余结束", "## a <!-- #a -->\n<!-- /pw:compare -->\n", []string{"unopened-container"}, []int{2}},
		{"缺 id", "## a <!-- #a -->\n<!-- pw:compare -->\n<!-- /pw:compare -->\n", []string{"missing-container-id"}, []int{2}},
		{"子项重复与缺锚点", "## a <!-- #a -->\n<!-- pw:scenarios id=\"s\" -->\n#### x <!-- #x -->\n#### y <!-- #x -->\n#### z\n<!-- /pw:scenarios -->\n", []string{"duplicate-item", "missing-item-anchor"}, []int{4, 5}},
		{"容器与区块 ID 重复", "## a <!-- #a -->\n<!-- pw:compare id=\"a\" -->\n<!-- /pw:compare -->\n", []string{"duplicate-id"}, []int{2}},
		{"子项 ID 不进文件命名空间", "## a <!-- #a -->\n<!-- pw:compare id=\"c\" -->\n#### x <!-- #a -->\n<!-- /pw:compare -->\n", nil, nil},
		{"缺解除条件", "## a <!-- #a -->\n<!-- pw:workaround id=\"w\" -->\n> 过渡\n<!-- /pw:workaround -->\n", []string{"missing-condition"}, []int{2}},
		{"代码块中的指令被忽略", "## a <!-- #a -->\n```\n<!-- pw:compare id=\"c\" -->\n```\n", nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := mustParse(t, tt.in)
			var lines []int
			for _, g := range d.Diags {
				lines = append(lines, g.Line)
			}
			if got := diagCodes(d); !slices.Equal(got, tt.diags) || !slices.Equal(lines, tt.lines) {
				t.Errorf("diags = %q %v, want %q %v", got, lines, tt.diags, tt.lines)
			}
		})
	}
	d := mustParse(t, tests[0].in)
	if bs := d.Section("a").Blocks; len(bs) != 1 || bs[0].Kind != BlockMarkdown {
		t.Errorf("未闭合容器应退化为普通 Markdown: %+v", bs)
	}
	d = mustParse(t, "<!-- pw:workaround id=\"w\" -->\n> **过渡做法(解除条件:接口上线)**:本地兜底\n<!-- /pw:workaround -->\n")
	if c := d.Container("w"); c == nil || c.Condition != "接口上线" {
		t.Errorf("前言区容器与半角符号: %+v", c)
	}
}

func TestRefs(t *testing.T) {
	in := "前言见 @flow#start\n\n## a <!-- #a -->\n详见@discover#tabs 与 (@pick-shop#closed/opt_a)。\n邮箱 me@corp#x 不算, `@code#span` 不算.\n\n```\n@code#block\n```\n<!-- @html#comment -->\n"
	d := mustParse(t, in)
	var all []string
	for _, r := range d.Refs {
		all = append(all, r.Target.String())
	}
	if want := []string{"flow#start", "discover#tabs", "pick-shop#closed/opt_a"}; !slices.Equal(all, want) {
		t.Errorf("refs = %q, want %q", all, want)
	}
	if rs := d.Section("a").Refs; len(rs) != 2 || rs[0].Line != 4 {
		t.Errorf("section refs = %+v", rs)
	}
}

func TestSetMeta(t *testing.T) {
	src := "id: discover # 模块 ID\n# 关联源码\nscope:\n  - a/**\nsections:\n  stage:\n    # 已确认\n    confirmed_hash: \"a84f21c09e12\"\n  tabs:\n    assumptions:\n      - id: x\n        text: 旧\nowner: me\n"
	f, err := ParseFront([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	f.SetMeta("tabs", &SectionMeta{ConfirmedHash: "123456789012", Assumptions: []Assumption{{"x", "旧"}, {"y", "新"}}})
	f.SetMeta("list", &SectionMeta{Retracted: []Assumption{{"z", "撤回"}}})
	want := "---\nid: discover # 模块 ID\n# 关联源码\nscope:\n  - a/**\nsections:\n  stage:\n    # 已确认\n    confirmed_hash: \"a84f21c09e12\"\n  tabs:\n    confirmed_hash: \"123456789012\"\n    assumptions:\n      - id: x\n        text: 旧\n      - id: y\n        text: \"新\"\n  list:\n    retracted:\n      - id: z\n        text: \"撤回\"\nowner: me\n---\n"
	if got := string(f.Encode()); got != want {
		t.Errorf("encode =\n%s\nwant\n%s", got, want)
	}
	g, err := ParseFront(f.Encode()[4 : len(f.Encode())-4])
	if err != nil || g.Sections["tabs"].ConfirmedHash != "123456789012" || !slices.Equal(g.SectionOrder, []string{"stage", "tabs", "list"}) {
		t.Errorf("reparse = %+v, %v", g, err)
	}
	f.SetMeta("stage", &SectionMeta{})
	f.SetMeta("tabs", nil)
	f.SetMeta("list", &SectionMeta{})
	if got := string(f.Encode()); got != "---\nid: discover # 模块 ID\n# 关联源码\nscope:\n  - a/**\nowner: me\n---\n" {
		t.Errorf("清空后 = %q", got)
	}
	if len(f.Sections) != 0 || len(f.SectionOrder) != 0 {
		t.Errorf("sections = %v %v", f.Sections, f.SectionOrder)
	}

	e, _ := ParseFront(nil)
	if e.Encode() != nil {
		t.Error("空映射应编码为 nil")
	}
	e.SetMeta("a", &SectionMeta{ConfirmedHash: "abc"})
	if got := string(e.Encode()); got != "---\nsections:\n  a:\n    confirmed_hash: \"abc\"\n---\n" {
		t.Errorf("新建 = %q", got)
	}
}

func TestFrontRoundTrip(t *testing.T) {
	d := mustParse(t, exampleFront+"## a <!-- #a -->\n")
	if got := string(d.Front.Encode()); got != exampleFront {
		t.Errorf("encode =\n%s", got)
	}
	d = mustParse(t, "---\r\n# 顶部注释\r\n\r\nid: x\r\n---\r\n## a <!-- #a -->\r\n")
	if d.Front.ID != "x" || d.FrontEnd != len("---\r\n# 顶部注释\r\n\r\nid: x\r\n---\r\n") || d.Sections[0].ID != "a" {
		t.Errorf("crlf front = %+v end=%d", d.Front, d.FrontEnd)
	}
	if got := string(d.Front.Encode()); got != "---\n# 顶部注释\n\nid: x\n---\n" {
		t.Errorf("doc comment = %q", got)
	}
	for _, bad := range []string{"---\nid: [\n---\n", "---\n- a\n---\n", "---\nid: x\n", "---\nsections: 1\n---\n"} {
		if _, err := Parse([]byte(bad), KindModule); err == nil {
			t.Errorf("Parse(%q) 应失败", bad)
		}
	}
}

func TestAssemble(t *testing.T) {
	tests := []struct {
		front  string
		chunks []string
		want   string
	}{
		{"---\nid: a\n---\n", []string{"\n# T\n\n", "## a\nx\n\n", "## b\ny\n"}, "---\nid: a\n---\n\n# T\n\n## a\nx\n\n## b\ny\n"},
		{"", []string{"## a\nx", "## b\ny\n\n\n"}, "## a\nx\n## b\ny\n\n\n"},
		{"---\n---", []string{"", "## a\nx\n", "## b\n"}, "---\n---\n## a\nx\n## b\n"},
	}
	for _, tt := range tests {
		var chunks [][]byte
		for _, c := range tt.chunks {
			chunks = append(chunks, []byte(c))
		}
		if got := string(Assemble([]byte(tt.front), chunks...)); got != tt.want {
			t.Errorf("Assemble = %q, want %q", got, tt.want)
		}
	}
}

func TestParseSectionInput(t *testing.T) {
	tests := []struct {
		in        string
		hasAssume bool
		assume    int
		heading   string
		id        string
		body      string
	}{
		{"正文\n", false, 0, "", "", "正文\n"},
		{"## 新标题 <!-- #tabs -->\n正文\n", false, 0, "## 新标题 <!-- #tabs -->\n", "tabs", "正文\n"},
		{"---\nassumptions:\n  - id: a\n    text: t\n---\n\n## 标题 <!-- #s -->", true, 1, "## 标题 <!-- #s -->\n", "s", ""},
		{"---\nassumptions: []\n---\n### 小标题\n", true, 0, "", "", "### 小标题\n"},
	}
	for _, tt := range tests {
		in, err := ParseSectionInput([]byte(tt.in))
		if err != nil {
			t.Fatal(err)
		}
		if in.HasAssumptions != tt.hasAssume || len(in.Assumptions) != tt.assume || string(in.Heading) != tt.heading || in.HeadingID != tt.id || string(in.Body) != tt.body {
			t.Errorf("ParseSectionInput(%q) = %+v", tt.in, in)
		}
	}
	if _, err := ParseSectionInput([]byte("---\nconfirmed_hash: x\n---\n")); err == nil {
		t.Error("只允许 assumptions")
	}
}

func TestParseTarget(t *testing.T) {
	tests := []struct {
		in   string
		want Target
		ok   bool
	}{
		{"discover", Target{Doc: "discover"}, true},
		{"@discover#tabs", Target{Doc: "discover", Section: "tabs"}, true},
		{"pick-shop#closed/opt_a", Target{Doc: "pick-shop", Section: "closed", Item: "opt_a"}, true},
		{"", Target{}, false},
		{"a#", Target{}, false},
		{"a#b/", Target{}, false},
		{"a#b/c/d", Target{}, false},
		{"a b#c", Target{}, false},
	}
	for _, tt := range tests {
		got, err := ParseTarget(tt.in)
		if (err == nil) != tt.ok || got != tt.want {
			t.Errorf("ParseTarget(%q) = %+v, %v", tt.in, got, err)
		}
		if tt.ok && got.String() != strings.TrimPrefix(tt.in, "@") {
			t.Errorf("String() = %q", got.String())
		}
	}
}

func TestConflictMarkers(t *testing.T) {
	const front = "---\nid: discover\n---\n"
	tests := []struct {
		name string
		in   string
		line int
	}{
		{"正文冲突", front + "<<<<<<< ours\n## 舞台 <!-- #stage -->\n甲\n=======\n## 舞台 <!-- #stage -->\n乙\n>>>>>>> theirs\n", 4},
		{"不带标签", front + "## 舞台 <!-- #stage -->\n<<<<<<<\n甲\n=======\n乙\n>>>>>>>\n", 5},
		{"Frontmatter 冲突", "---\n<<<<<<< ours\nid: a\n=======\nid: b\n>>>>>>> theirs\n---\n", 2},
		{"围栏代码块内的示例", front + "## 冲突 <!-- #c -->\n~~~text\n<<<<<<< ours\n甲\n=======\n乙\n>>>>>>> theirs\n~~~\n", 0},
		{"只有开始标记", front + "<<<<<<< ours\n甲\n", 0},
		{"多层引用而非结束标记", front + ">>>>>>> 引用\n", 0},
		{"更长的符号串", front + "<<<<<<<< 甲\n>>>>>>>> 乙\n", 0},
		{"一侧未闭合的围栏延续到另一侧", front + "<<<<<<< ours\n## 流程 <!-- #flow -->\n```mermaid\nA --> D\n=======\n## 流程 <!-- #flow -->\n```mermaid\nA --> C\n>>>>>>> theirs\n", 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.in), KindModule)
			if got := conflictLine([]byte(tt.in)); got != tt.line || (err != nil) != (tt.line > 0) {
				t.Errorf("line = %d, err = %v, want line %d", got, err, tt.line)
			}
		})
	}
}

func TestReadFront(t *testing.T) {
	tests := []struct {
		name, in string
		scope    []string
	}{
		{"正文冲突时 Frontmatter 照常读取", "---\nid: a\nscope:\n  - \"src/**\"\n---\n<<<<<<< ours\n甲\n=======\n乙\n>>>>>>> theirs\n", []string{"src/**"}},
		{"Frontmatter 冲突取 ours 一侧", "---\nid: a\n<<<<<<< ours\nscope:\n  - \"ours/**\"\n||||||| base\nscope: []\n=======\nscope:\n  - \"theirs/**\"\n>>>>>>> theirs\n---\n", []string{"ours/**"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := ReadFront([]byte(tt.in))
			if err != nil || f.ID != "a" || !slices.Equal(f.Scope, tt.scope) {
				t.Errorf("f = %+v, err = %v", f, err)
			}
		})
	}
}
