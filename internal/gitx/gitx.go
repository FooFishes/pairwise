// Package gitx 是对系统 git 命令的薄封装. 不使用 go-git, 保证行为与开发者日常操作一致.
package gitx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Repo 是一个 Git 工作树.
type Repo struct {
	// Root 是工作树根目录的绝对路径.
	Root string
	// GitDir 是当前工作树私有的 Git 目录, 多 Worktree 时为 .git/worktrees/<name>.
	GitDir string
	// CommonDir 是各工作树共享的 Git 目录.
	CommonDir string
}

// Open 从 dir 向上定位所在的工作树. 返回的路径均为解析过符号链接的绝对路径.
func Open(ctx context.Context, dir string) (*Repo, error) {
	out, err := run(ctx, dir, "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-dir", "--git-common-dir")
	if errors.Is(err, exec.ErrNotFound) {
		return nil, fmt.Errorf("未找到 git 命令.\n修复操作: 安装 Git 并确认 `git --version` 可执行后重试")
	}
	if err != nil {
		return nil, fmt.Errorf("%s 不在 Git 工作树中.\n修复操作: 进入仓库目录后重试; 新项目先执行 `git init`", dir)
	}
	f := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	return &Repo{Root: f[0], GitDir: f[1], CommonDir: f[2]}, nil
}

// Run 在工作树根目录执行 git, 返回 stdout; 失败时错误信息包含 stderr.
func (r *Repo) Run(ctx context.Context, args ...string) ([]byte, error) {
	return run(ctx, r.Root, args...)
}

// run 在 dir 中执行 git. 固定选项屏蔽用户的分页、颜色、路径转义与签名展示配置, 保证输出可解析;
// 环境变量原样继承, 因为提交钩子中 git 通过 GIT_INDEX_FILE 指向本次提交实际使用的暂存区.
func run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	full := append([]string{"-C", dir, "--no-pager",
		"-c", "core.quotepath=off", "-c", "color.ui=false", "-c", "log.showSignature=false"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			err = fmt.Errorf("%w: %s", err, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

// Branch 返回当前分支名; 分离头指针时返回 HEAD@<短 SHA>, 尚无提交的分支返回分支名.
func (r *Repo) Branch(ctx context.Context) (string, error) {
	if out, err := r.Run(ctx, "symbolic-ref", "--short", "-q", "HEAD"); err == nil {
		return strings.TrimSpace(string(out)), nil
	}
	out, err := r.Run(ctx, "rev-parse", "--short", "HEAD")
	if err != nil {
		return "", err
	}
	return "HEAD@" + strings.TrimSpace(string(out)), nil
}

// Detached 报告 Branch 返回的名称是否表示分离头指针.
func Detached(branch string) bool {
	return strings.HasPrefix(branch, "HEAD@")
}

// HeadPath 返回需要监听以感知分支切换的 HEAD 文件路径.
func (r *Repo) HeadPath() string {
	return r.GitDir + "/HEAD"
}

// ChangedFiles 返回相对工作树根目录的改动路径. cached 为真时比较暂存区与 HEAD
// (尚无提交时为全部暂存文件), 否则比较 baseRef...HEAD. 重命名按删除加新增处理.
func (r *Repo) ChangedFiles(ctx context.Context, cached bool, baseRef string) ([]string, error) {
	args := []string{"diff", "--name-only", "-z", "--no-renames"}
	if cached {
		args = append(args, "--cached")
	} else {
		args = append(args, baseRef+"...HEAD")
	}
	out, err := r.Run(ctx, append(args, "--")...)
	if err != nil {
		return nil, err
	}
	return splitNUL(out), nil
}

// MergeFile 在当前目录按行三路合并文件, 结果写回 ours, 冲突处写入 markerSize 个字符的冲突标记; 返回冲突数.
func MergeFile(ctx context.Context, ours, base, theirs string, markerSize int) (int, error) {
	_, err := run(ctx, ".", "merge-file", "--marker-size="+strconv.Itoa(markerSize), "-L", "ours", "-L", "base", "-L", "theirs", ours, base, theirs)
	// 退出码是冲突数 (超过 127 时为 127), 出错时为负数.
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() > 0 && ee.ExitCode() < 128 {
		return ee.ExitCode(), nil
	}
	return 0, err
}

// CommitMessages 返回 baseRef..HEAD 区间内每个提交的完整提交信息.
func (r *Repo) CommitMessages(ctx context.Context, baseRef string) ([]string, error) {
	out, err := r.Run(ctx, "log", "-z", "--format=%B", baseRef+"..HEAD", "--")
	if err != nil {
		return nil, err
	}
	return splitNUL(out), nil
}

// FileHistory 返回改动过 path 的提交 SHA, 从新到旧, 最多 limit 个. 尚无提交时返回空.
func (r *Repo) FileHistory(ctx context.Context, path string, limit int) ([]string, error) {
	out, err := r.Run(ctx, "rev-list", "--ignore-missing", "-n", strconv.Itoa(limit), "HEAD", "--", path)
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(out)), nil
}

// ShowFile 返回 rev 中 path 的内容. 使用 cat-file 读取原始字节, 不经过 textconv 等转换.
func (r *Repo) ShowFile(ctx context.Context, rev, path string) ([]byte, error) {
	return r.Run(ctx, "cat-file", "blob", rev+":"+path)
}

// SetConfig 写入本地仓库配置.
func (r *Repo) SetConfig(ctx context.Context, key, value string) error {
	_, err := r.Run(ctx, "config", "--local", key, value)
	return err
}

// HooksDir 返回钩子目录, 遵循 core.hooksPath.
func (r *Repo) HooksDir(ctx context.Context) (string, error) {
	out, err := r.Run(ctx, "rev-parse", "--path-format=absolute", "--git-path", "hooks")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// splitNUL 切分以 NUL 结尾的记录.
func splitNUL(b []byte) []string {
	parts := strings.Split(string(b), "\x00")
	return parts[:len(parts)-1]
}

// scissors 是 git commit -v 在提交信息模板中插入的剪切线, 其后内容不属于提交信息.
const scissors = "# ------------------------ >8 ------------------------"

// Trailers 解析提交信息中的指定 trailer 值, 如 Pair-Skip. key 不区分大小写, 空值不计入.
// 规则与 git interpret-trailers 一致: trailer 段是标题之后、以空行开头的最后一段, 且全部由
// trailer 行与其缩进续行组成, 或含 Git 生成的 trailer 且 trailer 行不少于 25%. 以 # 开头的
// 注释行与剪切线之后的内容先被剔除, 使 commit-msg 钩子收到的原始信息文件也能正确解析.
func Trailers(message, key string) []string {
	var lines []string
	for _, l := range strings.Split(message, "\n") {
		l = strings.TrimSuffix(l, "\r")
		if l == scissors {
			break
		}
		if !strings.HasPrefix(l, "#") {
			lines = append(lines, l)
		}
	}
	blank := func(l string) bool { return strings.TrimSpace(l) == "" }
	for len(lines) > 0 && blank(lines[len(lines)-1]) {
		lines = lines[:len(lines)-1]
	}
	start := len(lines)
	for start > 0 && !blank(lines[start-1]) {
		start--
	}
	if start == 0 {
		return nil
	}

	type trailer struct{ key, value string }
	var (
		ts        []trailer
		others    int
		generated bool
		// cont 表示上一个非缩进行是 trailer 行, 此时缩进行是它的续行.
		cont bool
	)
	for _, l := range lines[start:] {
		if l[0] == ' ' || l[0] == '\t' {
			if cont {
				ts[len(ts)-1].value += " " + strings.TrimSpace(l)
			} else {
				others++
			}
			continue
		}
		g := strings.HasPrefix(l, "Signed-off-by: ") || strings.HasPrefix(l, "(cherry picked from commit ")
		generated = generated || g
		i := separator(l)
		cont = g || i > 0
		switch {
		case i > 0:
			ts = append(ts, trailer{strings.TrimSpace(l[:i]), strings.TrimSpace(l[i+1:])})
		case g:
			ts = append(ts, trailer{})
		default:
			others++
		}
	}
	if others > 0 && (!generated || len(ts)*3 < others) {
		return nil
	}

	var values []string
	for _, t := range ts {
		if t.value != "" && strings.EqualFold(t.key, key) {
			values = append(values, t.value)
		}
	}
	return values
}

// EmptyMessage 报告提交信息去掉 key trailer 行后按 git 的规则是否为空: 只剩空白行与 Signed-off-by 行.
// editor 表示提交信息经过编辑器, 此时 git 还会去掉以 # 开头的注释行与剪切线之后的内容.
func EmptyMessage(message, key string, editor bool) bool {
	for _, l := range strings.Split(message, "\n") {
		l = strings.TrimSuffix(l, "\r")
		if editor && l == scissors {
			break
		}
		i := separator(l)
		switch {
		case strings.TrimSpace(l) == "", editor && strings.HasPrefix(l, "#"), strings.HasPrefix(l, "Signed-off-by:"):
		case i > 0 && strings.EqualFold(strings.TrimSpace(l[:i]), key):
		default:
			return false
		}
	}
	return true
}

// separator 返回 trailer 行中冒号的位置: 冒号前须是由 ASCII 字母、数字与连字符组成的 key,
// key 与冒号之间可有空白; 不符合时返回 -1.
func separator(l string) int {
	space := false
	for i, c := range l {
		switch {
		case c == ':':
			return i
		case !space && (c == '-' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'):
		case c == ' ' || c == '\t':
			space = true
		default:
			return -1
		}
	}
	return -1
}
