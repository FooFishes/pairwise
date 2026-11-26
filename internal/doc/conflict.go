package doc

import (
	"bytes"
	"regexp"
)

// fenceRe 匹配围栏代码块的开始或结束行.
var fenceRe = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})")

// conflictLine 返回第一处未解决的合并冲突所在行号, 没有时返回 0. 与 git 一致, 冲突以行首 7 个 < 开始、
// 7 个 > 结束; 围栏代码块内的开始标记是示例, 不算冲突.
func conflictLine(raw []byte) int {
	start, n := 0, 0
	var fence []byte
	for i := 0; i < len(raw); {
		line, next := nextLine(raw, i)
		i, n = next, n+1
		// 冲突开始后结束标记不受围栏影响: 一侧打开的围栏可能在另一侧才闭合, 或一直延续到文件末尾.
		if start > 0 && isMarker(line, '>') {
			return start
		}
		if m := fenceRe.FindSubmatch(line); m != nil {
			switch {
			case fence == nil:
				fence = m[1]
			case m[1][0] == fence[0] && len(m[1]) >= len(fence) && len(bytes.TrimSpace(line[len(m[0]):])) == 0:
				fence = nil
			}
			continue
		}
		if fence == nil && start == 0 && isMarker(line, '<') {
			start = n
		}
	}
	return 0
}

// isMarker 判断 line 是否为由 7 个 c 组成的冲突标记行, 其后只能是行尾或空白开头的标签.
func isMarker(line []byte, c byte) bool {
	if len(line) < 7 || bytes.Count(line[:7], []byte{c}) != 7 {
		return false
	}
	return len(line) == 7 || line[7] == ' ' || line[7] == '\t'
}

// ReadFront 读取文档的 Frontmatter, 供无法解析的文档 (如正文含冲突标记) 沿用 scope 与受控字段.
// Frontmatter 内的冲突取 ours 一侧, 即当前分支的值.
func ReadFront(raw []byte) (*Front, error) {
	text, _, err := splitFront(raw)
	if err != nil {
		return nil, err
	}
	return ParseFront(oursSide(text))
}

// oursSide 去掉冲突标记行与 base、theirs 两侧的内容, 只保留 ours 一侧.
func oursSide(text []byte) []byte {
	var out []byte
	in, keep := false, true
	for i := 0; i < len(text); {
		line, next := nextLine(text, i)
		switch {
		case isMarker(line, '<'):
			in, keep = true, true
		case in && (isMarker(line, '|') || isMarker(line, '=')):
			keep = false
		case in && isMarker(line, '>'):
			in, keep = false, true
		case keep:
			out = append(out, text[i:next]...)
		}
		i = next
	}
	return out
}

var (
	wideOpen  = regexp.MustCompile(`(?m)^<{7,}(?:[ \t][^\n]*)?\r?$`)
	wideClose = regexp.MustCompile(`(?m)^>{7,}(?:[ \t][^\n]*)?\r?$`)
)

// HasMarkers 报告 raw 中是否有任意长度的冲突标记 (行首 7 个以上的 < 及其后 7 个以上的 >), 不豁免围栏代码块.
// 交叉合并时 git 合并多个合并基得到的虚拟 base 用更长的标记 (随递归层数增长), 且可能落在围栏内.
func HasMarkers(raw []byte) bool {
	o := wideOpen.FindIndex(raw)
	return o != nil && wideClose.Match(raw[o[1]:])
}
