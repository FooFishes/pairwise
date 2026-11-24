// Package merge 实现文档级三路合并, 供 pair update 与 pair merge-driver 共用.
//
// Frontmatter 按 YAML 键递归合并, 元素都带 id 的序列按 id 合并;
// 正文以前言区与各 ## 区块为单位按区块 ID 合并. 同一键或同一区块两侧都有改动且结果不同即冲突.
// 未改动的区块与 Frontmatter 原样保留字节.
package merge

import (
	"github.com/FooFishes/pairwise/internal/doc"
	_ "github.com/aymanbagabas/go-udiff"
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
	panic("TODO")
}

// Diff 生成 a 到 b 的统一格式差异, 无差异时返回空字符串.
func Diff(nameA, nameB string, a, b []byte) string {
	panic("TODO")
}
