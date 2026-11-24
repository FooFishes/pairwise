// Package doc 解析与写回 .pair/ 下的 Markdown 文档.
//
// 读写一律基于原文字节区间切分与拼接, 不经过语法树往返; goldmark 只用来可靠地定位
// 代码块、标题与 HTML 注释, 避免把代码块或注释中的 ## 误判为区块边界.
package doc

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"go.yaml.in/yaml/v3"
)

// Kind 是由所在目录决定的文档类型.
type Kind int

const (
	KindModule Kind = iota + 1
	KindFlow
	KindDecision
)

// Dir 返回该类型在 .pair/ 下的目录名.
func (k Kind) Dir() string {
	switch k {
	case KindModule:
		return "modules"
	case KindFlow:
		return "flows"
	case KindDecision:
		return "decisions"
	}
	return ""
}

// String 返回 module、flow 或 decision.
func (k Kind) String() string {
	switch k {
	case KindModule:
		return "module"
	case KindFlow:
		return "flow"
	case KindDecision:
		return "decision"
	}
	return ""
}

// Span 是 Document.Raw 中的半开区间 [Start, End).
type Span struct {
	Start, End int
}

// Assumption 是 Agent 自行补充、待开发者确认的一条默认细节.
type Assumption struct {
	ID   string `yaml:"id"`
	Text string `yaml:"text"`
}

// SectionMeta 是 Frontmatter sections 表中某个区块的确认元数据.
type SectionMeta struct {
	ConfirmedHash string       `yaml:"confirmed_hash,omitempty"`
	Assumptions   []Assumption `yaml:"assumptions,omitempty"`
	Retracted     []Assumption `yaml:"retracted,omitempty"`
}

// Empty 报告元数据是否没有任何内容, 空元数据写回时从 sections 表中移除.
func (m *SectionMeta) Empty() bool {
	return m == nil || (m.ConfirmedHash == "" && len(m.Assumptions) == 0 && len(m.Retracted) == 0)
}

// Front 是 Frontmatter 的结构化视图.
//
// Node 保留解析出的 YAML 映射节点, 写回时只在 Node 上就地修改受管的键,
// 以保留其余键的顺序与注释.
type Front struct {
	ID            string
	Scope         []string
	Targets       []string
	DefaultOption string
	Sections      map[string]*SectionMeta
	// SectionOrder 是 sections 表的键序.
	SectionOrder []string
	// Node 是 Frontmatter 的映射节点, 文档没有 Frontmatter 时为空映射.
	Node *yaml.Node
	// doc 是解析出的文档节点, 携带首个键之前的注释, 编码时用它包裹 Node.
	doc *yaml.Node
}

// Status 是区块的确认状态.
type Status int

const (
	StatusUnconfirmed Status = iota + 1
	StatusConfirmed
	StatusChanged
)

// BlockKind 是区块正文中预设组件的类型.
type BlockKind int

const (
	BlockMarkdown BlockKind = iota + 1
	BlockMermaid
	BlockLayout
	BlockCompare
	BlockScenarios
	BlockWorkaround
)

// LayoutNode 是 pw-layout 缩进树的一个节点.
type LayoutNode struct {
	Label    string
	Children []*LayoutNode
}

// Item 是 pw:compare 的方案或 pw:scenarios 的场景, 由 #### 标题 <!-- #id --> 开始.
type Item struct {
	ID    string
	Title string
	// Span 从 #### 行首到下一个子项或容器结束注释之前.
	Span Span
	// Body 是标题行之后的正文.
	Body   Span
	Blocks []Block
	Line   int
}

// Block 是正文按预设组件切分后的一段, 相邻的普通 Markdown 合并为一个 BlockMarkdown.
type Block struct {
	Kind BlockKind
	// Span 覆盖整段原文, 容器类包含首尾两条注释.
	Span Span
	// ID 是容器的 id 属性.
	ID string
	// Code 是 mermaid 与 pw-layout 代码块的源码.
	Code   string
	Layout []*LayoutNode
	Items  []Item
	// Condition 是 pw:workaround 引用块中的解除条件.
	Condition string
	Line      int
}

// Ref 是正文中形如 @doc#sec 或 @doc#container/item 的引用.
type Ref struct {
	Target Target
	Line   int
}

// Section 是由 ## 标题 <!-- #id --> 开始的扁平受管区块.
type Section struct {
	// ID 是锚点, 标题缺少锚点时为空.
	ID string
	// Title 是去掉锚点注释后的标题文字.
	Title string
	// Span 从 ## 行首到下一个 ## 行首或文件末尾.
	Span Span
	// Body 是标题行之后的正文.
	Body Span
	// Line 是标题所在行号, 从 1 开始.
	Line   int
	Hash   string
	Blocks []Block
	Refs   []Ref
}

// Severity 是诊断的严重程度.
type Severity int

const (
	SeverityError Severity = iota + 1
	SeverityWarning
)

// Diagnostic 是解析或校验发现的问题.
type Diagnostic struct {
	Severity Severity
	Line     int
	Code     string
	Message  string
	// Fix 是可直接执行的修复指引.
	Fix string
}

// Document 是解析后的 .pair 文档, 所有区间都指向 Raw.
type Document struct {
	Raw  []byte
	Kind Kind
	// Front 永不为空; 文档没有 Frontmatter 时为零值加空映射节点.
	Front *Front
	// FrontEnd 之前是含首尾 --- 分隔线的 Frontmatter, 没有 Frontmatter 时为 0.
	FrontEnd int
	// Title 是前言区第一个一级标题的文字.
	Title string
	// Preamble 是 Frontmatter 之后、第一个 ## 之前的前言区.
	Preamble       Span
	PreambleBlocks []Block
	Sections       []*Section
	// Refs 汇总前言区与全部区块中的引用.
	Refs  []Ref
	Diags []Diagnostic
}

// Parse 解析一份文档. Frontmatter 不是合法 YAML 时返回错误, 其余语法问题记入 Diags.
func Parse(raw []byte, kind Kind) (*Document, error) {
	text, end, err := splitFront(raw)
	if err != nil {
		return nil, err
	}
	front, err := ParseFront(text)
	if err != nil {
		return nil, err
	}
	d := &Document{Raw: raw, Kind: kind, Front: front, FrontEnd: end}
	newParser(d).parse()
	return d, nil
}

// Section 按 ID 查找区块, 不存在时返回 nil.
func (d *Document) Section(id string) *Section {
	for _, s := range d.Sections {
		if id != "" && s.ID == id {
			return s
		}
	}
	return nil
}

// Text 返回区间对应的原文.
func (d *Document) Text(s Span) []byte {
	return d.Raw[s.Start:s.End]
}

// Meta 返回区块的确认元数据, 没有时返回 nil.
func (d *Document) Meta(id string) *SectionMeta {
	return d.Front.Sections[id]
}

// Status 根据 confirmed_hash 计算区块的确认状态.
func (d *Document) Status(s *Section) Status {
	m := d.Meta(s.ID)
	switch {
	case m == nil || m.ConfirmedHash == "":
		return StatusUnconfirmed
	case m.ConfirmedHash == s.Hash:
		return StatusConfirmed
	}
	return StatusChanged
}

// Item 按容器 ID 与子项 ID 在全文中查找子项, 不存在时返回 nil.
func (d *Document) Item(container, id string) *Item {
	c := d.Container(container)
	if c == nil || id == "" {
		return nil
	}
	for i := range c.Items {
		if c.Items[i].ID == id {
			return &c.Items[i]
		}
	}
	return nil
}

// Container 按 ID 在全文中查找容器块, 不存在时返回 nil.
func (d *Document) Container(id string) *Block {
	if id == "" {
		return nil
	}
	groups := [][]Block{d.PreambleBlocks}
	for _, s := range d.Sections {
		groups = append(groups, s.Blocks)
	}
	for _, bs := range groups {
		for i := range bs {
			if bs[i].ID == id {
				return &bs[i]
			}
		}
	}
	return nil
}

// Normalize 按确认哈希的归一化规则处理文本: 统一换行符为 \n, 去除行尾空白与首尾空行,
// 严格保留行首缩进与正文空白.
func Normalize(b []byte) []byte {
	lines := bytes.Split(bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n")), []byte("\n"))
	for i, l := range lines {
		lines[i] = bytes.TrimRightFunc(l, unicode.IsSpace)
	}
	for len(lines) > 0 && len(lines[0]) == 0 {
		lines = lines[1:]
	}
	for len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	return bytes.Join(lines, []byte("\n"))
}

// Hash 返回归一化后内容的 SHA-256 前 12 位十六进制, 用作 confirmed_hash.
func Hash(b []byte) string {
	return FileHash(Normalize(b))
}

// FileHash 返回文件原始字节的 SHA-256 前 12 位十六进制, 用作 --base.
func FileHash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])[:12]
}

// ParseFront 解析不含 --- 分隔线的 Frontmatter 文本.
func ParseFront(text []byte) (*Front, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(text, &root); err != nil {
		return nil, fmt.Errorf("Frontmatter 不是合法的 YAML: %w", err)
	}
	f := &Front{Node: &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}}
	if root.Kind == yaml.DocumentNode {
		f.doc = &root
		if len(root.Content) > 0 {
			f.Node = root.Content[0]
		}
	}
	if f.Node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("Frontmatter 必须是 YAML 映射")
	}
	var v struct {
		ID            string                  `yaml:"id"`
		Scope         []string                `yaml:"scope"`
		Targets       []string                `yaml:"targets"`
		DefaultOption string                  `yaml:"default_option"`
		Sections      map[string]*SectionMeta `yaml:"sections"`
	}
	if err := f.Node.Decode(&v); err != nil {
		return nil, fmt.Errorf("Frontmatter 字段类型不正确: %w", err)
	}
	f.ID, f.Scope, f.Targets, f.DefaultOption, f.Sections = v.ID, v.Scope, v.Targets, v.DefaultOption, v.Sections
	if i := mapIndex(f.Node, "sections"); i >= 0 {
		secs := f.Node.Content[i+1]
		for j := 0; j+1 < len(secs.Content); j += 2 {
			f.SectionOrder = append(f.SectionOrder, secs.Content[j].Value)
		}
	}
	return f, nil
}

// SetMeta 设置区块的确认元数据并同步到 Node; m 为空元数据时从 sections 表中移除该区块.
// 新区块追加在 sections 表末尾, 已有区块保留原位置.
func (f *Front) SetMeta(id string, m *SectionMeta) {
	i := mapIndex(f.Node, "sections")
	if m.Empty() {
		delete(f.Sections, id)
		f.SectionOrder = slices.DeleteFunc(f.SectionOrder, func(s string) bool { return s == id })
		if i < 0 {
			return
		}
		secs := f.Node.Content[i+1]
		if j := mapIndex(secs, id); j >= 0 {
			secs.Content = slices.Delete(secs.Content, j, j+2)
		}
		if len(secs.Content) == 0 {
			f.Node.Content = slices.Delete(f.Node.Content, i, i+2)
		}
		return
	}
	if f.Sections == nil {
		f.Sections = map[string]*SectionMeta{}
	}
	f.Sections[id] = m
	if !slices.Contains(f.SectionOrder, id) {
		f.SectionOrder = append(f.SectionOrder, id)
	}
	if i < 0 {
		f.Node.Content = append(f.Node.Content, plain("sections"), &yaml.Node{})
		i = len(f.Node.Content) - 2
	}
	secs := asMapping(f.Node.Content[i+1])
	j := mapIndex(secs, id)
	if j < 0 {
		secs.Content = append(secs.Content, plain(id), &yaml.Node{})
		j = len(secs.Content) - 2
	}
	setMetaNode(asMapping(secs.Content[j+1]), m)
}

// Encode 把 Node 整段编码为含首尾 --- 分隔线的 Frontmatter 文本, 缩进为两个空格, 别名展开为副本.
// Node 为空映射时返回 nil. 写回已有文档时用 Render 沿用原文字节.
func (f *Front) Encode() []byte {
	if len(f.Node.Content) == 0 {
		return nil
	}
	root := yaml.Node{Kind: yaml.DocumentNode}
	if f.doc != nil {
		root = *f.doc
	}
	root.Content = []*yaml.Node{expand(f.Node, nil)}
	var b bytes.Buffer
	b.WriteString("---\n")
	e := yaml.NewEncoder(&b)
	e.SetIndent(2)
	// 节点树来自解析或本包构造, 编码失败只可能是程序缺陷.
	if err := e.Encode(&root); err != nil {
		panic(err)
	}
	b.WriteString("---\n")
	return b.Bytes()
}

// Assemble 依次拼接 Frontmatter 与正文片段, 前一段不以换行结尾时补一个换行, 其余字节原样保留.
func Assemble(front []byte, chunks ...[]byte) []byte {
	out := slices.Clone(front)
	for _, c := range chunks {
		if len(c) == 0 {
			continue
		}
		if len(out) > 0 && out[len(out)-1] != '\n' {
			out = append(out, '\n')
		}
		out = append(out, c...)
	}
	return out
}

// SectionInput 是 pair update doc#sec 的输入.
type SectionInput struct {
	// HasAssumptions 为真时 Assumptions 整体替换该区块的假设列表.
	HasAssumptions bool
	Assumptions    []Assumption
	// Heading 是输入首行的 ## 标题行(含换行), 输入不以标题开头时为空.
	Heading []byte
	// HeadingID 是 Heading 中的锚点.
	HeadingID string
	Body      []byte
}

// ParseSectionInput 解析区块输入: 可选的只含 assumptions 的 Frontmatter, 可选的 ## 标题行, 其后为正文.
func ParseSectionInput(b []byte) (*SectionInput, error) {
	text, end, err := splitFront(b)
	if err != nil {
		return nil, err
	}
	in := &SectionInput{}
	if end > 0 {
		f, err := ParseFront(text)
		if err != nil {
			return nil, err
		}
		for i := 0; i < len(f.Node.Content); i += 2 {
			if k := f.Node.Content[i].Value; k != "assumptions" {
				return nil, fmt.Errorf("区块输入的 Frontmatter 只能包含 assumptions, 不能包含 %s", k)
			}
		}
		var v struct {
			Assumptions []Assumption `yaml:"assumptions"`
		}
		if err := f.Node.Decode(&v); err != nil {
			return nil, fmt.Errorf("assumptions 格式不正确, 应为 id 与 text 组成的列表: %w", err)
		}
		in.HasAssumptions, in.Assumptions = len(f.Node.Content) > 0, v.Assumptions
	}
	rest := b[end:]
	i := 0
	for i < len(rest) {
		line, next := nextLine(rest, i)
		if len(bytes.TrimSpace(line)) > 0 {
			break
		}
		i = next
	}
	line, next := nextLine(rest, i)
	if m := atxRe.FindSubmatch(line); m != nil && len(m[1]) == 2 {
		in.Heading = rest[i:next]
		if !bytes.HasSuffix(in.Heading, []byte("\n")) {
			in.Heading = append(slices.Clone(in.Heading), '\n')
		}
		_, in.HeadingID = splitAnchor(strings.TrimSpace(string(m[2])))
		in.Body = rest[next:]
		return in, nil
	}
	in.Body = rest
	return in, nil
}
