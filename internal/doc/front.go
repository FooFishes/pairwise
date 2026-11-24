package doc

import (
	"bytes"
	"errors"
	"reflect"
	"slices"

	"go.yaml.in/yaml/v3"
)

// splitFront 切出文件开头 --- 行与下一个 --- 行之间的 Frontmatter 文本, end 是结束分隔线之后的偏移;
// 文件不以 --- 行开头时 end 为 0.
func splitFront(raw []byte) (text []byte, end int, err error) {
	line, start := nextLine(raw, 0)
	if string(line) != "---" {
		return nil, 0, nil
	}
	for i := start; i < len(raw); {
		line, next := nextLine(raw, i)
		if string(line) == "---" {
			return raw[start:i], next, nil
		}
		i = next
	}
	return nil, 0, errors.New("文档的 Frontmatter 缺少结束分隔线 ---")
}

// nextLine 返回从 i 开始的一行 (不含换行符) 与下一行的起点.
func nextLine(b []byte, i int) ([]byte, int) {
	j := bytes.IndexByte(b[i:], '\n')
	if j < 0 {
		return bytes.TrimSuffix(b[i:], []byte("\r")), len(b)
	}
	return bytes.TrimSuffix(b[i:i+j], []byte("\r")), i + j + 1
}

// mapIndex 返回映射节点中键 key 的下标, 不存在时返回 -1.
func mapIndex(m *yaml.Node, key string) int {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return i
		}
	}
	return -1
}

func plain(s string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}
}

// quoted 构造双引号字符串, 避免形如全数字的哈希被解读为数字.
func quoted(s string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s, Style: yaml.DoubleQuotedStyle}
}

// asMapping 把空值节点就地改为空映射.
func asMapping(n *yaml.Node) *yaml.Node {
	if n.Kind != yaml.MappingNode {
		n.Kind, n.Tag, n.Value, n.Style, n.Content = yaml.MappingNode, "!!map", "", 0, nil
	}
	return n
}

// setMetaNode 按 confirmed_hash、assumptions、retracted 的顺序重排区块元数据节点,
// 沿用已有的键节点与未变化的值节点以保留注释与风格.
func setMetaNode(n *yaml.Node, m *SectionMeta) {
	old := &yaml.Node{Content: n.Content}
	n.Content = nil
	put := func(key string, empty bool, value func(old *yaml.Node) *yaml.Node) {
		if empty {
			return
		}
		k, v := plain(key), (*yaml.Node)(nil)
		if i := mapIndex(old, key); i >= 0 {
			k, v = old.Content[i], old.Content[i+1]
		}
		n.Content = append(n.Content, k, value(v))
	}
	put("confirmed_hash", m.ConfirmedHash == "", func(v *yaml.Node) *yaml.Node {
		if v != nil && v.Kind == yaml.ScalarNode && v.Value == m.ConfirmedHash {
			return v
		}
		return quoted(m.ConfirmedHash)
	})
	put("assumptions", len(m.Assumptions) == 0, func(v *yaml.Node) *yaml.Node { return assumptionsNode(v, m.Assumptions) })
	put("retracted", len(m.Retracted) == 0, func(v *yaml.Node) *yaml.Node { return assumptionsNode(v, m.Retracted) })
}

// assumptionsNode 构造假设列表节点, 内容不变的条目沿用原节点.
func assumptionsNode(old *yaml.Node, list []Assumption) *yaml.Node {
	n := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	var prev []*yaml.Node
	if old != nil && old.Kind == yaml.SequenceNode {
		n, prev = old, old.Content
	}
	n.Content = nil
	for _, a := range list {
		var item *yaml.Node
		for _, p := range prev {
			var x Assumption
			if p.Decode(&x) == nil && x == a {
				item = p
				break
			}
		}
		if item == nil {
			item = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
				plain("id"), plain(a.ID), plain("text"), quoted(a.Text),
			}}
		}
		n.Content = append(n.Content, item)
	}
	return n
}

// Render 把 Node 编码为含首尾 --- 分隔线的 Frontmatter, 尽量沿用原文字节: 顶层键的值与某份原文 srcs
// (含分隔线的 Frontmatter 原文, 靠前的优先) 中语义相同时, 原样沿用该原文中这个键的行 (含其前的注释与空行),
// 其余键单独编码后拼入, 使 Git Diff 只出现真正修改的键. 原文都不是逐行书写的块映射时退回整段编码.
func (f *Front) Render(srcs ...[]byte) []byte {
	if len(f.Node.Content) == 0 {
		return nil
	}
	var ls []*frontLines
	for _, src := range srcs {
		if l := splitKeys(src); l != nil {
			ls = append(ls, l)
		}
	}
	if len(ls) == 0 || f.Node.Style&yaml.FlowStyle != 0 {
		return f.Encode()
	}
	out := slices.Clone(ls[0].open)
	for i := 0; i+1 < len(f.Node.Content); i += 2 {
		k, v := f.Node.Content[i], f.Node.Content[i+1]
		var lead []byte
		var body []byte
		for _, l := range ls {
			c, ok := l.keys[k.Value]
			if !ok {
				continue
			}
			if lead == nil {
				lead = c.lead
			}
			// 含别名的原文行依赖别处的锚点, 不单独沿用.
			if !c.alias && Equal(c.value, v) {
				lead, body = c.lead, c.body
				break
			}
		}
		if body == nil {
			body = encodePair(k, v, lead != nil, ls[0].eol)
		}
		out = append(append(out, lead...), body...)
	}
	return append(append(out, ls[0].tail...), ls[0].close...)
}

// frontLines 是一段 Frontmatter 原文按顶层键切开的行.
type frontLines struct {
	open, close, tail []byte
	eol               string
	keys              map[string]chunk
}

// chunk 是一个顶层键的原文: lead 是键之前的整行注释与空行, body 从键所在行到值的最后一行.
type chunk struct {
	lead, body []byte
	value      *yaml.Node
	alias      bool
}

// splitKeys 切分 Frontmatter 原文; 不是每个顶层键都从行首开始的块映射时返回 nil.
func splitKeys(raw []byte) *frontLines {
	text, end, err := splitFront(raw)
	if err != nil || end == 0 {
		return nil
	}
	var root yaml.Node
	if yaml.Unmarshal(text, &root) != nil {
		return nil
	}
	_, open := nextLine(raw, 0)
	l := &frontLines{open: raw[:open], close: raw[open+len(text) : end], eol: "\n", keys: map[string]chunk{}}
	if bytes.Contains(raw[:end], []byte("\r\n")) {
		l.eol = "\r\n"
	}
	lines := bytes.SplitAfter(text, []byte("\n"))
	if len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	var m *yaml.Node
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		m = root.Content[0]
		if m.Kind != yaml.MappingNode || m.Style&yaml.FlowStyle != 0 {
			return nil
		}
	}
	prev := 0 // 上一个键的值结束于此行之前
	for i := 0; m != nil && i+1 < len(m.Content); i += 2 {
		k := m.Content[i]
		start, next := k.Line-1, len(lines)
		if i+3 < len(m.Content) {
			next = m.Content[i+2].Line - 1
		}
		if k.Kind != yaml.ScalarNode || k.Column != 1 || start < prev || next <= start || l.keys[k.Value].body != nil {
			return nil
		}
		stop := next
		for stop > start+1 && filler(lines[stop-1]) {
			stop--
		}
		v := m.Content[i+1]
		l.keys[k.Value] = chunk{lead: bytes.Join(lines[prev:start], nil), body: bytes.Join(lines[start:stop], nil), value: v, alias: hasAlias(v)}
		prev = stop
	}
	l.tail = bytes.Join(lines[prev:], nil)
	return l
}

// filler 报告一行是否为空行或从行首开始的注释: 这样的行出现在两个顶层键之间时归属后一个键.
func filler(line []byte) bool {
	t := bytes.TrimRight(line, " \t\r\n")
	return len(t) == 0 || t[0] == '#'
}

func hasAlias(n *yaml.Node) bool {
	return n.Kind == yaml.AliasNode || slices.ContainsFunc(n.Content, hasAlias)
}

// encodePair 单独编码一个顶层键值对. inherited 表示键前的注释与值后的整行注释已沿用原文, 编码时去掉以免重复.
func encodePair(k, v *yaml.Node, inherited bool, eol string) []byte {
	kc, vc := *k, *expand(v, nil)
	if inherited {
		kc.HeadComment, kc.FootComment, vc.FootComment = "", "", ""
	}
	var b bytes.Buffer
	e := yaml.NewEncoder(&b)
	e.SetIndent(2)
	// 节点来自解析或本包构造, 编码失败只可能是程序缺陷.
	if err := e.Encode(&yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{&kc, &vc}}); err != nil {
		panic(err)
	}
	if eol != "\n" {
		return bytes.ReplaceAll(b.Bytes(), []byte("\n"), []byte(eol))
	}
	return b.Bytes()
}

// expand 返回把别名替换为其目标副本、去掉锚点的深拷贝, 使分开编码的片段不会引用未输出的锚点.
// visiting 记录正在展开的别名目标, 自引用的别名原样保留.
func expand(n *yaml.Node, visiting map[*yaml.Node]bool) *yaml.Node {
	if n.Kind == yaml.AliasNode && n.Alias != nil && !visiting[n.Alias] {
		if visiting == nil {
			visiting = map[*yaml.Node]bool{}
		}
		visiting[n.Alias] = true
		defer delete(visiting, n.Alias)
		return expand(n.Alias, visiting)
	}
	c := *n
	c.Anchor = ""
	c.Content = make([]*yaml.Node, len(n.Content))
	for i, x := range n.Content {
		c.Content[i] = expand(x, visiting)
	}
	return &c
}

// Equal 按语义值比较两个 YAML 节点, 忽略风格与注释; nil 表示不存在.
func Equal(a, b *yaml.Node) bool {
	if a == nil || b == nil {
		return a == b
	}
	var x, y any
	if a.Decode(&x) != nil || b.Decode(&y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}
