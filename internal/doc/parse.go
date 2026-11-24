package doc

import (
	"bytes"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

const idPattern = `[A-Za-z0-9_-]+`

var (
	idRe        = regexp.MustCompile(`^` + idPattern + `$`)
	atxRe       = regexp.MustCompile(`^ {0,3}(#{1,6})(?:[ \t]+(.*?))?[ \t]*$`)
	anchorRe    = regexp.MustCompile(`^(.*?)[ \t]*<!--[ \t]*#(` + idPattern + `)[ \t]*-->$`)
	directiveRe = regexp.MustCompile(`^ {0,3}<!--[ \t]*(/?)pw:(compare|scenarios|workaround)(?:[ \t]+id="([^"\n]*)")?[ \t]*-->$`)
	conditionRe = regexp.MustCompile(`(?m)^ {0,3}>[ \t]*\*\*过渡做法[（(]解除条件[：:][ \t]*(.+?)[）)]\*\*`)
	refRe       = regexp.MustCompile(`@(` + idPattern + `)#(` + idPattern + `)(?:/(` + idPattern + `))?`)
	// layoutPrefixRe 匹配 pw-layout 行首的缩进、树形连线与列表标记.
	layoutPrefixRe = regexp.MustCompile("^[ \t│├└┬┼─|`+]*(?:-{2,}[ \t]*)?(?:[-*][ \t]+)?")
)

var containerKinds = map[string]BlockKind{
	"compare":    BlockCompare,
	"scenarios":  BlockScenarios,
	"workaround": BlockWorkaround,
}

func containerName(k BlockKind) string {
	for name, v := range containerKinds {
		if v == k {
			return name
		}
	}
	return ""
}

// node 是文档级 AST 子节点及其在 Raw 中的行区间, end 是下一个兄弟节点的行首.
type node struct {
	n          ast.Node
	start, end int
}

// opening 是尚未闭合的容器开始注释.
type opening struct {
	kind BlockKind
	id   string
	at   int
	line int
}

type parser struct {
	d *Document
	// src 是 Frontmatter 之后的正文, goldmark 的偏移都相对它, 加上 off 即为 Raw 中的偏移.
	src        []byte
	off        int
	lineStarts []int
	// ids 是文件级命名空间中已出现的 ID 及其行号.
	ids map[string]int
}

func newParser(d *Document) *parser {
	p := &parser{d: d, src: d.Raw[d.FrontEnd:], off: d.FrontEnd, lineStarts: []int{0}, ids: map[string]int{}}
	for i, c := range d.Raw {
		if c == '\n' {
			p.lineStarts = append(p.lineStarts, i+1)
		}
	}
	return p
}

func (p *parser) parse() {
	d := p.d
	root := goldmark.New().Parser().Parse(text.NewReader(p.src))
	var nodes []node
	for n := root.FirstChild(); n != nil; n = n.NextSibling() {
		nodes = append(nodes, node{n: n, start: p.lineStart(p.off + n.Pos())})
	}
	for i := range nodes {
		nodes[i].end = len(d.Raw)
		if i+1 < len(nodes) {
			nodes[i].end = nodes[i+1].start
		}
	}
	var heads []int
	for i, n := range nodes {
		if p.headingLevel(n) == 2 {
			heads = append(heads, i)
		}
	}
	pre := nodes
	d.Preamble = Span{d.FrontEnd, len(d.Raw)}
	if len(heads) > 0 {
		pre, d.Preamble.End = nodes[:heads[0]], nodes[heads[0]].start
	}
	for _, n := range pre {
		if h, ok := n.n.(*ast.Heading); ok && h.Level == 1 {
			d.Title, _ = p.heading(h)
			break
		}
	}
	d.PreambleBlocks = p.blocks(pre, d.Preamble, true)
	for k, hi := range heads {
		next, end := len(nodes), len(d.Raw)
		if k+1 < len(heads) {
			next, end = heads[k+1], nodes[heads[k+1]].start
		}
		h := nodes[hi]
		s := &Section{Span: Span{h.start, end}, Body: Span{p.lineEnd(h.start), end}, Line: p.lineNo(h.start)}
		s.Title, s.ID = p.heading(h.n)
		if s.ID == "" {
			p.diag(SeverityError, s.Line, "missing-anchor",
				fmt.Sprintf("二级标题「%s」缺少锚点", s.Title),
				fmt.Sprintf("在标题行末尾补上锚点, 如 `## %s <!-- #<id> -->`, id 只能包含字母、数字、- 与 _", s.Title))
		} else {
			p.claim(s.ID, s.Line)
		}
		s.Hash = Hash(d.Text(s.Span))
		s.Blocks = p.blocks(nodes[hi+1:next], s.Body, true)
		d.Sections = append(d.Sections, s)
	}
	p.refs(root)
}

// blocks 把区间内的文档级节点切成 Blocks; containers 为假时不识别容器注释 (用于子项正文).
func (p *parser) blocks(nodes []node, region Span, containers bool) []Block {
	var out []Block
	md := -1
	flush := func(end int) {
		if md >= 0 {
			if s := p.trimBlank(Span{md, end}); s.Start < s.End {
				out = append(out, Block{Kind: BlockMarkdown, Span: s, Line: p.lineNo(s.Start)})
			}
		}
		md = -1
	}
	var open *opening
	for i, n := range nodes {
		if containers {
			if kind, closing, id, ok := p.directive(n); ok {
				line := p.lineNo(n.start)
				name := containerName(kind)
				switch {
				case !closing && open == nil:
					if !idRe.MatchString(id) {
						p.diag(SeverityError, line, "missing-container-id",
							fmt.Sprintf("pw:%s 容器缺少合法的 id 属性", name),
							fmt.Sprintf("改为 `<!-- pw:%s id=\"<id>\" -->`, id 只能包含字母、数字、- 与 _", name))
						id = ""
					}
					open = &opening{kind: kind, id: id, at: i, line: line}
					continue
				case !closing:
					p.diag(SeverityError, line, "nested-container",
						fmt.Sprintf("pw:%s 不能嵌套在第 %d 行打开的 pw:%s 内", name, open.line, containerName(open.kind)),
						fmt.Sprintf("先用 `<!-- /pw:%s -->` 结束外层容器", containerName(open.kind)))
				case open == nil:
					p.diag(SeverityError, line, "unopened-container",
						fmt.Sprintf("结束注释 /pw:%s 没有对应的开始注释", name),
						fmt.Sprintf("删除该行, 或在容器内容之前补上 `<!-- pw:%s id=\"<id>\" -->`", name))
				case kind != open.kind:
					p.diag(SeverityError, line, "mismatched-container",
						fmt.Sprintf("结束注释 /pw:%s 与第 %d 行打开的 pw:%s 不匹配", name, open.line, containerName(open.kind)),
						fmt.Sprintf("改为 `<!-- /pw:%s -->`", containerName(open.kind)))
				default:
					start := nodes[open.at].start
					flush(start)
					out = append(out, p.container(open, nodes[open.at+1:i], Span{start, p.trimBlank(Span{n.start, n.end}).End}, n.start))
					open = nil
					continue
				}
			}
		}
		if open != nil {
			continue
		}
		if b, ok := p.code(n); ok {
			flush(n.start)
			out = append(out, b)
			continue
		}
		if md < 0 {
			md = n.start
		}
	}
	if open != nil {
		name := containerName(open.kind)
		p.diag(SeverityError, open.line, "unclosed-container",
			fmt.Sprintf("pw:%s 容器未闭合", name),
			fmt.Sprintf("在所属区块结束前补上 `<!-- /pw:%s -->`", name))
		if md < 0 {
			md = nodes[open.at].start
		}
	}
	flush(region.End)
	return out
}

// container 构造容器块, inner 是首尾注释之间的节点, closeAt 是结束注释的行首.
func (p *parser) container(o *opening, inner []node, span Span, closeAt int) Block {
	b := Block{Kind: o.kind, Span: span, ID: o.id, Line: o.line}
	if b.ID != "" {
		p.claim(b.ID, b.Line)
	}
	if o.kind == BlockWorkaround {
		for _, n := range inner {
			if _, ok := n.n.(*ast.Blockquote); ok {
				if m := conditionRe.FindSubmatch(p.d.Raw[n.start:n.end]); m != nil {
					b.Condition = strings.TrimSpace(string(m[1]))
					break
				}
			}
		}
		if b.Condition == "" {
			p.diag(SeverityWarning, b.Line, "missing-condition",
				fmt.Sprintf("pw:workaround「%s」缺少解除条件", b.ID),
				"在容器内写入 `> **过渡做法（解除条件：<条件>）**：<说明>`")
		}
		return b
	}
	var heads []int
	for i, n := range inner {
		if p.headingLevel(n) == 4 {
			heads = append(heads, i)
		}
	}
	seen := map[string]bool{}
	for k, hi := range heads {
		next, end := len(inner), closeAt
		if k+1 < len(heads) {
			next, end = heads[k+1], inner[heads[k+1]].start
		}
		h := inner[hi]
		it := Item{Span: Span{h.start, end}, Body: Span{p.lineEnd(h.start), end}, Line: p.lineNo(h.start)}
		it.Title, it.ID = p.heading(h.n)
		switch {
		case it.ID == "":
			p.diag(SeverityError, it.Line, "missing-item-anchor",
				fmt.Sprintf("容器「%s」的子项「%s」缺少锚点", b.ID, it.Title),
				fmt.Sprintf("在标题行末尾补上锚点, 如 `#### %s <!-- #<id> -->`", it.Title))
		case seen[it.ID]:
			p.diag(SeverityError, it.Line, "duplicate-item",
				fmt.Sprintf("容器「%s」内子项 ID「%s」重复", b.ID, it.ID),
				"为其中一个子项改用容器内唯一的 ID")
		}
		seen[it.ID] = true
		it.Blocks = p.blocks(inner[hi+1:next], it.Body, false)
		b.Items = append(b.Items, it)
	}
	return b
}

// directive 识别独占一行的 pw: 容器注释.
func (p *parser) directive(n node) (kind BlockKind, closing bool, id string, ok bool) {
	if _, isHTML := n.n.(*ast.HTMLBlock); !isHTML {
		return 0, false, "", false
	}
	m := directiveRe.FindSubmatch(bytes.TrimRight(p.d.Raw[n.start:n.end], " \t\r\n"))
	if m == nil {
		return 0, false, "", false
	}
	return containerKinds[string(m[2])], len(m[1]) > 0, string(m[3]), true
}

// code 识别 mermaid 与 pw-layout 围栏代码块.
func (p *parser) code(n node) (Block, bool) {
	fc, ok := n.n.(*ast.FencedCodeBlock)
	if !ok {
		return Block{}, false
	}
	var code strings.Builder
	for i := 0; i < fc.Lines().Len(); i++ {
		seg := fc.Lines().At(i)
		code.Write(seg.Value(p.src))
	}
	b := Block{Span: p.trimBlank(Span{n.start, n.end}), Code: code.String(), Line: p.lineNo(n.start)}
	switch string(fc.Language(p.src)) {
	case "mermaid":
		b.Kind = BlockMermaid
	case "pw-layout":
		b.Kind, b.Layout = BlockLayout, parseLayout(b.Code)
	default:
		return Block{}, false
	}
	return b, true
}

// parseLayout 按标签起始列把缩进树解析为节点: 每个节点挂在它之前最近的、起始列更小的节点下.
func parseLayout(code string) []*LayoutNode {
	type entry struct {
		col int
		n   *LayoutNode
	}
	var roots []*LayoutNode
	var stack []entry
	for _, line := range strings.Split(code, "\n") {
		line = strings.TrimRight(line, " \t\r")
		prefix := layoutPrefixRe.FindString(line)
		if len(prefix) == len(line) {
			continue
		}
		e := entry{utf8.RuneCountInString(prefix), &LayoutNode{Label: line[len(prefix):]}}
		for len(stack) > 0 && stack[len(stack)-1].col >= e.col {
			stack = stack[:len(stack)-1]
		}
		if len(stack) == 0 {
			roots = append(roots, e.n)
		} else {
			parent := stack[len(stack)-1].n
			parent.Children = append(parent.Children, e.n)
		}
		stack = append(stack, e)
	}
	return roots
}

// refs 在正文中查找引用, 跳过代码块、行内代码与 HTML.
func (p *parser) refs(root ast.Node) {
	var excl []Span
	add := func(s, e int) { excl = append(excl, Span{s, e}) }
	addLines := func(n ast.Node) {
		for i := 0; i < n.Lines().Len(); i++ {
			add(n.Lines().At(i).Start, n.Lines().At(i).Stop)
		}
	}
	_ = ast.Walk(root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.FencedCodeBlock:
			addLines(n)
			if n.Info != nil {
				add(n.Info.Segment.Start, n.Info.Segment.Stop)
			}
			return ast.WalkSkipChildren, nil
		case *ast.CodeBlock:
			addLines(n)
			return ast.WalkSkipChildren, nil
		case *ast.HTMLBlock:
			addLines(n)
			if n.HasClosure() {
				add(n.ClosureLine.Start, n.ClosureLine.Stop)
			}
			return ast.WalkSkipChildren, nil
		case *ast.CodeSpan:
			for c := n.FirstChild(); c != nil; c = c.NextSibling() {
				if t, ok := c.(*ast.Text); ok {
					add(t.Segment.Start, t.Segment.Stop)
				}
			}
			return ast.WalkSkipChildren, nil
		case *ast.RawHTML:
			for i := 0; i < n.Segments.Len(); i++ {
				add(n.Segments.At(i).Start, n.Segments.At(i).Stop)
			}
		}
		return ast.WalkContinue, nil
	})
	d := p.d
	for _, m := range refRe.FindAllSubmatchIndex(p.src, -1) {
		if m[0] > 0 && isAlnum(p.src[m[0]-1]) || overlaps(excl, m[0], m[1]) {
			continue
		}
		t := Target{Doc: string(p.src[m[2]:m[3]]), Section: string(p.src[m[4]:m[5]])}
		if m[6] >= 0 {
			t.Item = string(p.src[m[6]:m[7]])
		}
		at := p.off + m[0]
		r := Ref{Target: t, Line: p.lineNo(at)}
		d.Refs = append(d.Refs, r)
		if i := sort.Search(len(d.Sections), func(i int) bool { return d.Sections[i].Span.Start > at }); i > 0 {
			d.Sections[i-1].Refs = append(d.Sections[i-1].Refs, r)
		}
	}
}

func overlaps(spans []Span, s, e int) bool {
	for _, x := range spans {
		if s < x.End && x.Start < e {
			return true
		}
	}
	return false
}

func isAlnum(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// headingLevel 返回文档级 ATX 标题的级别, 其他节点 (含 Setext 标题) 返回 0.
func (p *parser) headingLevel(n node) int {
	if _, ok := n.n.(*ast.Heading); !ok {
		return 0
	}
	line, _ := nextLine(p.d.Raw, n.start)
	m := atxRe.FindSubmatch(line)
	if m == nil {
		return 0
	}
	return len(m[1])
}

// heading 返回标题去掉锚点后的文字与锚点 ID.
func (p *parser) heading(n ast.Node) (title, id string) {
	var b strings.Builder
	for i := 0; i < n.Lines().Len(); i++ {
		seg := n.Lines().At(i)
		b.Write(seg.Value(p.src))
	}
	return splitAnchor(strings.TrimSpace(b.String()))
}

func splitAnchor(s string) (title, id string) {
	if m := anchorRe.FindStringSubmatch(s); m != nil {
		return strings.TrimSpace(m[1]), m[2]
	}
	return s, ""
}

// claim 在文件级命名空间登记 ID, 重复时记一条诊断.
func (p *parser) claim(id string, line int) {
	if first, ok := p.ids[id]; ok {
		p.diag(SeverityError, line, "duplicate-id",
			fmt.Sprintf("ID「%s」在文件内重复, 首次出现于第 %d 行", id, first),
			"区块锚点与顶层容器 id 共享文件级命名空间, 为其中一处改用唯一的 ID")
		return
	}
	p.ids[id] = line
}

func (p *parser) diag(sev Severity, line int, code, msg, fix string) {
	p.d.Diags = append(p.d.Diags, Diagnostic{Severity: sev, Line: line, Code: code, Message: msg, Fix: fix})
}

func (p *parser) lineNo(off int) int {
	return sort.Search(len(p.lineStarts), func(i int) bool { return p.lineStarts[i] > off })
}

func (p *parser) lineStart(off int) int {
	return p.lineStarts[p.lineNo(off)-1]
}

func (p *parser) lineEnd(off int) int {
	_, next := nextLine(p.d.Raw, off)
	return next
}

// trimBlank 去掉区间首尾的空白行, 结果的终点落在最后一个非空行的换行之后.
func (p *parser) trimBlank(s Span) Span {
	raw := p.d.Raw
	for s.Start < s.End {
		line, next := nextLine(raw[:s.End], s.Start)
		if len(bytes.TrimSpace(line)) > 0 {
			break
		}
		s.Start = next
	}
	for s.End > s.Start {
		ls := s.Start + bytes.LastIndexByte(raw[s.Start:s.End-1], '\n') + 1
		if len(bytes.TrimSpace(raw[ls:s.End])) > 0 {
			break
		}
		s.End = ls
	}
	return s
}
