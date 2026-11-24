// Package gitx 是对系统 git 命令的薄封装. 不使用 go-git, 保证行为与开发者日常操作一致.
package gitx

import "context"

// Repo 是一个 Git 工作树.
type Repo struct {
	// Root 是工作树根目录的绝对路径.
	Root string
	// GitDir 是当前工作树私有的 Git 目录, 多 Worktree 时为 .git/worktrees/<name>.
	GitDir string
	// CommonDir 是各工作树共享的 Git 目录.
	CommonDir string
}

// Open 从 dir 向上定位所在的工作树.
func Open(ctx context.Context, dir string) (*Repo, error) {
	panic("TODO")
}

// Run 在工作树根目录执行 git, 返回 stdout; 失败时错误信息包含 stderr.
func (r *Repo) Run(ctx context.Context, args ...string) ([]byte, error) {
	panic("TODO")
}

// Branch 返回当前分支名; 分离头指针时返回 HEAD@<短 SHA>, 尚无提交的分支返回分支名.
func (r *Repo) Branch(ctx context.Context) (string, error) {
	panic("TODO")
}

// HeadPath 返回需要监听以感知分支切换的 HEAD 文件路径.
func (r *Repo) HeadPath() string {
	return r.GitDir + "/HEAD"
}

// ChangedFiles 返回相对工作树根目录的改动路径. cached 为真时比较暂存区与 HEAD,
// 否则比较 baseRef...HEAD. 重命名按删除加新增处理.
func (r *Repo) ChangedFiles(ctx context.Context, cached bool, baseRef string) ([]string, error) {
	panic("TODO")
}

// CommitMessages 返回 baseRef..HEAD 区间内每个提交的完整提交信息.
func (r *Repo) CommitMessages(ctx context.Context, baseRef string) ([]string, error) {
	panic("TODO")
}

// FileHistory 返回改动过 path 的提交 SHA, 从新到旧, 最多 limit 个.
func (r *Repo) FileHistory(ctx context.Context, path string, limit int) ([]string, error) {
	panic("TODO")
}

// ShowFile 返回 rev 中 path 的内容.
func (r *Repo) ShowFile(ctx context.Context, rev, path string) ([]byte, error) {
	panic("TODO")
}

// SetConfig 写入本地仓库配置.
func (r *Repo) SetConfig(ctx context.Context, key, value string) error {
	panic("TODO")
}

// HooksDir 返回钩子目录, 遵循 core.hooksPath.
func (r *Repo) HooksDir(ctx context.Context) (string, error) {
	panic("TODO")
}

// Trailers 解析提交信息中的指定 trailer 值, 如 Pair-Skip.
func Trailers(message, key string) []string {
	panic("TODO")
}
