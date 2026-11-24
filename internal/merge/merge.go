// Package merge 实现文档级三路合并, 供 pair update 与 pair merge-driver 共用.
//
// Frontmatter 按 YAML 键递归合并, 元素都带 id 的序列按 id 合并;
// 正文以前言区与各 ## 区块为单位按区块 ID 合并. 同一键或同一区块两侧都有改动且结果不同即冲突.
// 未改动的区块与 Frontmatter 原样保留字节.
package merge

import (
	"bytes"
	"fmt"
	"reflect"
	"slices"

	"github.com/FooFishes/pairwise/internal/doc"
	"github.com/aymanbagabas/go-udiff"
	"go.yaml.in/yaml/v3"
)

// Conflict 描述一处无法自动合并的改动.
type Conflict struct {
	// Where 是冲突位置, 如 frontmatter:sections.tabs.assumptions、section:tabs 或 preamble.
	Where              string
	Base, Ours, Theirs []byte
}

// Labels 是写入冲突标记时三方的名称.
type Labels struct {
	Base, Ours, Theirs string
}

// Result 是合并结果.
type Result struct {
	// Raw 是合并后的完整文档; 存在冲突时冲突处写入 git 风格的冲突标记.
	Raw       []byte
	Conflicts []Conflict
}

// Merge3 对同一文档的三个版本做三路合并. base 为 nil 表示双方各自新建.
func Merge3(base, ours, theirs *doc.Document, labels Labels) (*Result, error) {
	bp, err := parts(base)
	if err != nil {
		return nil, err
	}
	op, err := parts(ours)
	if err != nil {
		return nil, err
	}
	tp, err := parts(theirs)
	if err != nil {
		return nil, err
	}
	r := &Result{}
	front := r.front(base, ours, theirs, labels)
	var ps []piece
	for _, k := range order(op.keys, tp.keys) {
		b, o, t := bp.text[k], op.text[k], tp.text[k]
		switch choose(b, o, t, sameText) {
		case useOurs:
			ps = append(ps, piece{k, deref(o), true})
		case useTheirs:
			ps = append(ps, piece{k, deref(t), false})
		default:
			where := "section:" + k
			if k == preamble {
				where = "preamble"
			}
			r.Conflicts = append(r.Conflicts, Conflict{Where: where, Base: deref(b), Ours: deref(o), Theirs: deref(t)})
			ps = append(ps, piece{k, markers(deref(o), deref(t), labels), false})
		}
	}
	r.Raw = doc.Assemble(front, joinPieces(ps, op.keys)...)
	return r, nil
}

// piece 是合并结果中的一个正文单元, fromOurs 表示字节原样取自 ours.
type piece struct {
	key      string
	text     []byte
	fromOurs bool
}

// joinPieces 返回各单元的字节. 只在合并后新出现的相邻处 (单元不是原样取自 ours, 或其后继与 ours 中不同)
// 补齐一个空行, ours 中原本相邻的单元字节不变.
func joinPieces(ps []piece, oursKeys []string) [][]byte {
	next := map[string]string{}
	for i := 0; i+1 < len(oursKeys); i++ {
		next[oursKeys[i]] = oursKeys[i+1]
	}
	ps = slices.DeleteFunc(ps, func(p piece) bool { return len(p.text) == 0 })
	out := make([][]byte, len(ps))
	for i, p := range ps {
		out[i] = p.text
		if i+1 < len(ps) && !(p.fromOurs && next[p.key] == ps[i+1].key) && !bytes.HasSuffix(p.text, []byte("\n\n")) {
			out[i] = append(bytes.TrimRight(slices.Clone(p.text), "\n"), "\n\n"...)
		}
	}
	return out
}

// Diff 生成 a 到 b 的统一格式差异, 无差异时返回空字符串.
func Diff(nameA, nameB string, a, b []byte) string {
	if bytes.Equal(a, b) {
		return ""
	}
	return udiff.Unified(nameA, nameB, string(a), string(b))
}

// preamble 是前言区的键; 区块 ID 与 "title:" 前缀的标题键都不会与它相同.
const preamble = ""

// version 是一个版本按顺序排列的正文单元.
type version struct {
	keys []string
	text map[string]*[]byte
}

// parts 把文档切成前言区与各区块; 区块按 ID 对齐, 缺 ID 时按标题文字对齐.
func parts(d *doc.Document) (version, error) {
	v := version{text: map[string]*[]byte{}}
	if d == nil {
		return v, nil
	}
	add := func(k string, b []byte) error {
		if _, dup := v.text[k]; dup {
			return fmt.Errorf("区块 %s 在文档中重复出现, 无法按区块合并; 请先修正重复的锚点或标题", k)
		}
		v.keys, v.text[k] = append(v.keys, k), &b
		return nil
	}
	_ = add(preamble, d.Text(d.Preamble))
	for _, s := range d.Sections {
		k := s.ID
		if k == "" {
			k = "title:" + s.Title
		}
		if err := add(k, d.Text(s.Span)); err != nil {
			return v, err
		}
	}
	return v, nil
}

func deref(b *[]byte) []byte {
	if b == nil {
		return nil
	}
	return *b
}

func sameText(a, b *[]byte) bool {
	if a == nil || b == nil {
		return a == b
	}
	return bytes.Equal(doc.Normalize(*a), doc.Normalize(*b))
}

type side int

const (
	useOurs side = iota + 1
	useTheirs
	conflict
)

// choose 对单个条目做三路选择, nil 表示该版本中不存在: 两侧相同或只有 ours 改动取 ours,
// 只有 theirs 改动取 theirs, 其余为冲突.
func choose[T any](b, o, t *T, eq func(x, y *T) bool) side {
	switch {
	case eq(o, t), eq(b, t):
		return useOurs
	case eq(b, o):
		return useTheirs
	}
	return conflict
}

// order 以 ours 的键序为准, 把 theirs 独有的键插在其在 theirs 中最近的、已在结果中的前驱之后.
func order(ours, theirs []string) []string {
	out := slices.Clone(ours)
	for i, k := range theirs {
		if slices.Contains(out, k) {
			continue
		}
		pos := 0
		for j := i - 1; j >= 0; j-- {
			if p := slices.Index(out, theirs[j]); p >= 0 {
				pos = p + 1
				break
			}
		}
		out = slices.Insert(out, pos, k)
	}
	return out
}

// markers 生成 git 风格的冲突标记.
func markers(o, t []byte, l Labels) []byte {
	var b bytes.Buffer
	line := func(s []byte) {
		if s = bytes.TrimRight(s, "\n"); len(s) > 0 {
			b.Write(s)
			b.WriteByte('\n')
		}
	}
	fmt.Fprintf(&b, "<<<<<<< %s\n", l.Ours)
	line(o)
	b.WriteString("=======\n")
	line(t)
	fmt.Fprintf(&b, ">>>>>>> %s\n", l.Theirs)
	return b.Bytes()
}

// front 合并 Frontmatter: 与 ours 语义相同时原样保留 ours 的字节, 冲突时整段用冲突标记包裹.
func (r *Result) front(base, ours, theirs *doc.Document, l Labels) []byte {
	var b *yaml.Node
	if base != nil {
		b = base.Front.Node
	}
	n := len(r.Conflicts)
	m := r.node(b, ours.Front.Node, theirs.Front.Node, "")
	switch {
	case len(r.Conflicts) > n:
		return append(append([]byte("---\n"), markers(frontText(ours), frontText(theirs), l)...), "---\n"...)
	case equal(m, ours.Front.Node):
		return ours.Raw[:ours.FrontEnd]
	}
	f := *ours.Front
	f.Node = m
	return f.Encode()
}

// frontText 返回 Frontmatter 首尾 --- 分隔线之间的原文.
func frontText(d *doc.Document) []byte {
	if d.FrontEnd == 0 {
		return nil
	}
	raw := d.Raw[:d.FrontEnd]
	inner := raw[bytes.IndexByte(raw, '\n')+1:]
	return inner[:bytes.LastIndexByte(bytes.TrimSuffix(inner, []byte("\n")), '\n')+1]
}

// node 三路合并 YAML 节点, nil 表示不存在. 两侧都改的映射按键递归, 元素都带 id 的序列按 id 递归,
// 其余记为冲突并返回 ours. 合并只构造新节点, 不修改输入.
func (r *Result) node(b, o, t *yaml.Node, path string) *yaml.Node {
	switch choose(b, o, t, equal) {
	case useOurs:
		return o
	case useTheirs:
		return t
	}
	if o != nil && t != nil {
		if isMap(o) && isMap(t) && (b == nil || isMap(b)) {
			return r.mapping(b, o, t, path)
		}
		if isKeyed(o) && isKeyed(t) && (b == nil || isKeyed(b)) {
			return r.sequence(b, o, t, path)
		}
	}
	r.Conflicts = append(r.Conflicts, Conflict{Where: "frontmatter:" + path, Base: encode(b), Ours: encode(o), Theirs: encode(t)})
	return o
}

func (r *Result) mapping(b, o, t *yaml.Node, path string) *yaml.Node {
	_, bv := entries(b)
	okeys, ov := entries(o)
	tkeys, tv := entries(t)
	n := *o
	n.Content = nil
	for _, k := range order(okeys, tkeys) {
		v := r.node(bv[k].value(), ov[k].value(), tv[k].value(), join(path, k))
		if v == nil {
			continue
		}
		key := ov[k].key()
		if key == nil {
			key = tv[k].key()
		}
		n.Content = append(n.Content, key, v)
	}
	return &n
}

func (r *Result) sequence(b, o, t *yaml.Node, path string) *yaml.Node {
	_, bi, _ := keyed(b)
	oids, oi, _ := keyed(o)
	tids, ti, _ := keyed(t)
	n := *o
	n.Content = nil
	for _, id := range order(oids, tids) {
		if v := r.node(bi[id], oi[id], ti[id], join(path, id)); v != nil {
			n.Content = append(n.Content, v)
		}
	}
	return &n
}

// pair 是映射中的一个键值对.
type pair [2]*yaml.Node

func (p *pair) key() *yaml.Node {
	if p == nil {
		return nil
	}
	return p[0]
}

func (p *pair) value() *yaml.Node {
	if p == nil {
		return nil
	}
	return p[1]
}

func entries(m *yaml.Node) ([]string, map[string]*pair) {
	if m == nil {
		return nil, nil
	}
	var keys []string
	out := map[string]*pair{}
	for i := 0; i+1 < len(m.Content); i += 2 {
		k := m.Content[i].Value
		keys = append(keys, k)
		out[k] = &pair{m.Content[i], m.Content[i+1]}
	}
	return keys, out
}

// keyed 报告 n 是否为元素全是带唯一标量 id 键的映射的序列, 并按 id 索引元素.
func keyed(n *yaml.Node) ([]string, map[string]*yaml.Node, bool) {
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil, nil, false
	}
	var ids []string
	out := map[string]*yaml.Node{}
	for _, e := range n.Content {
		if !isMap(e) {
			return nil, nil, false
		}
		_, kv := entries(e)
		id := kv["id"].value()
		if id == nil || id.Kind != yaml.ScalarNode || out[id.Value] != nil {
			return nil, nil, false
		}
		ids, out[id.Value] = append(ids, id.Value), e
	}
	return ids, out, true
}

func isKeyed(n *yaml.Node) bool {
	_, _, ok := keyed(n)
	return ok
}

func isMap(n *yaml.Node) bool {
	return n.Kind == yaml.MappingNode
}

// equal 按语义值比较两个节点, 忽略风格与注释.
func equal(a, b *yaml.Node) bool {
	if a == nil || b == nil {
		return a == b
	}
	var x, y any
	if a.Decode(&x) != nil || b.Decode(&y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

func encode(n *yaml.Node) []byte {
	if n == nil {
		return nil
	}
	b, _ := yaml.Marshal(n)
	return b
}

func join(path, k string) string {
	if path == "" {
		return k
	}
	return path + "." + k
}
