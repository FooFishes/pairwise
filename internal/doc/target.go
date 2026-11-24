package doc

import (
	"fmt"
	"strings"
)

// Target 是 doc、doc#sec 或 doc#container/item 形式的定位路径.
type Target struct {
	Doc     string
	Section string
	// Item 只在容器局部命名空间的路径中出现, 此时 Section 是容器 ID.
	Item string
}

// ParseTarget 解析定位路径, 允许带前导 @.
func ParseTarget(s string) (Target, error) {
	id, rest, hasSec := strings.Cut(strings.TrimPrefix(s, "@"), "#")
	t := Target{Doc: id}
	var hasItem bool
	if hasSec {
		t.Section, t.Item, hasItem = strings.Cut(rest, "/")
	}
	if !idRe.MatchString(t.Doc) || hasSec && !idRe.MatchString(t.Section) || hasItem && !idRe.MatchString(t.Item) {
		return Target{}, fmt.Errorf("定位路径 %q 不合法: 应为 doc、doc#sec 或 doc#container/item, 各段 ID 只能包含字母、数字、- 与 _", s)
	}
	return t, nil
}

// String 返回规范形式, 不带前导 @.
func (t Target) String() string {
	s := t.Doc
	if t.Section != "" {
		s += "#" + t.Section
	}
	if t.Item != "" {
		s += "/" + t.Item
	}
	return s
}
