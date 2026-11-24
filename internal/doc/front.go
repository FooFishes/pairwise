package doc

import (
	"bytes"
	"errors"

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
	return nil, 0, errors.New("Frontmatter 缺少结束分隔线 ---")
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
