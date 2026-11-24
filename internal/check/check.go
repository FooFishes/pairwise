// Package check 实现语法与引用校验以及文件级联动兜底检查, 不依赖常驻进程.
package check

import (
	"context"

	"github.com/FooFishes/pairwise/internal/doc"
	"github.com/FooFishes/pairwise/internal/repo"
)

// Diagnostic 是带文件路径的校验结果.
type Diagnostic struct {
	doc.Diagnostic
	// Path 是相对工作树根目录的路径.
	Path string
}

// Options 控制校验范围.
type Options struct {
	// Strict 把警告视为错误, 并在存在任何 decisions/*.md 时报错.
	Strict bool
}

// Run 校验索引中的全部文档.
func Run(ix *repo.Index, opts Options) []Diagnostic {
	panic("TODO")
}

// DiffOptions 控制 check --diff 的比较范围.
type DiffOptions struct {
	// BaseRef 为空时检查暂存区, 否则检查 BaseRef...HEAD.
	BaseRef string
	// Message 是暂存区模式下待提交的提交信息, 用于读取 Pair-Skip trailer.
	Message string
}

// Missing 是源码有改动但模块文档未更新的模块.
type Missing struct {
	DocID string
	// Path 是模块文档相对路径.
	Path string
	// Files 是命中该模块 scope 的改动源码.
	Files []string
}

// DiffResult 是 check --diff 的结果.
type DiffResult struct {
	Changed     []string
	Missing     []Missing
	SkipReasons []string
}

// OK 报告检查是否通过: 没有漏更新的模块, 或提交信息带有 Pair-Skip trailer.
func (r *DiffResult) OK() bool {
	return len(r.Missing) == 0 || len(r.SkipReasons) > 0
}

// Diff 找出改动源码命中 scope 但对应模块文档未改动的模块.
func Diff(ctx context.Context, r *repo.Repo, ix *repo.Index, opts DiffOptions) (*DiffResult, error) {
	panic("TODO")
}
