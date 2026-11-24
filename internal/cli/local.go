package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	pairv1 "github.com/FooFishes/pairwise/gen/pair/v1"
	"github.com/FooFishes/pairwise/internal/check"
	"github.com/FooFishes/pairwise/internal/client"
	"github.com/FooFishes/pairwise/internal/doc"
	"github.com/FooFishes/pairwise/internal/gitx"
	"github.com/FooFishes/pairwise/internal/merge"
	"github.com/FooFishes/pairwise/internal/repo"
)

// 本文件中的命令在进程内执行, 不经常驻进程: CI 与 git merge 期间不应拉起后台进程.

// mergeAttr 是 .pair/.gitattributes 中注册合并驱动的规则.
const mergeAttr = "*.md merge=pair"

// hookScript 是 init --git-hooks 写入的钩子脚本, %s 为钩子名.
const hookScript = "#!/bin/sh\n# 由 pair init --git-hooks 生成.\nexec pair git-hook %s \"$@\"\n"

var gitHooks = []string{"prepare-commit-msg", "commit-msg"}

// migrationGuide 是 init 输出的全仓迁移任务指引.
const migrationGuide = `全仓迁移任务 (由 Agent 按顺序执行):
1. 梳理拓扑: 扫描源码与旧 docs/, 按页面或核心子系统拆分模块, 跨模块的完整交互链路或调用时序拆为流程.
   - 模块: ` + "`pair update <id> --kind module --file <doc.md>`" + `, Frontmatter 含 id 与 scope (关联源码的 Glob 列表).
   - 流程: ` + "`pair update <id> --kind flow --file <doc.md>`" + `, Frontmatter 只含 id, 正文用 ` + "`@module#sec`" + ` 引用关联区块.
   - 正文按 ` + "`## 标题 <!-- #sec -->`" + ` 切分区块; Agent 自行补全、未经开发者确认的细节逐条写入 sections.<sec>.assumptions (id 与 text), 不写 confirmed_hash.
2. 归拢遗留问题: 业务类代码 TODO 迁入 ` + "`<!-- pw:workaround id=\"...\" -->`" + ` 块并写明解除条件; 旧文档之间或与代码不符之处写成待确认问题 (含 targets、default_option 与 pw:compare 方案对比), 用 ` + "`pair ask <id> --file <doc.md>`" + ` 提交.
3. 校验: 执行 ` + "`pair check`" + ` 修复全部错误, 用 ` + "`pair lookup <path...>`" + ` 抽查源码能反查到模块.
4. 归档旧文档: 核对迁移完整后, 归档或移除原有的重复长篇综述文档.
5. 审阅: 开发者执行 ` + "`pair serve --open`" + ` 在工作台审阅区块与假设并确认.
`

func (a *app) initCmd() *cobra.Command {
	var hooks bool
	cmd := &cobra.Command{
		Use:   "init [--git-hooks]",
		Short: "初始化 .pair/, 注册合并驱动, 可选安装 Git 提交钩子, 并输出全仓迁移指引",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			dir, err := os.Getwd()
			if err != nil {
				return err
			}
			r, err := repo.Open(ctx, dir)
			if err != nil && !errors.Is(err, repo.ErrNotInitialized) {
				return err
			}
			var t text
			t.line("已初始化 Pairwise: %s", r.PairDir)
			for _, k := range []doc.Kind{doc.KindModule, doc.KindFlow, doc.KindDecision} {
				if err := os.MkdirAll(filepath.Join(r.PairDir, k.Dir()), 0o755); err != nil {
					return err
				}
			}
			cfg := filepath.Join(r.PairDir, "config.toml")
			switch created, err := writeNew(cfg, repo.DefaultConfigTOML()); {
			case err != nil:
				return err
			case created:
				t.line("- 已创建 %s", r.Rel(cfg))
			default:
				t.line("- 保留已有的 %s", r.Rel(cfg))
			}
			if err := ensureLine(filepath.Join(r.PairDir, ".gitattributes"), mergeAttr); err != nil {
				return err
			}
			if err := r.Git.SetConfig(ctx, "merge.pair.name", "Pairwise 结构化三路合并"); err != nil {
				return err
			}
			if err := r.Git.SetConfig(ctx, "merge.pair.driver", "pair merge-driver %O %A %B"); err != nil {
				return err
			}
			t.line("- 已注册 Git 合并驱动 merge.pair (.pair/.gitattributes: %s)", mergeAttr)
			if !hooks {
				t.line("- 未安装 Git 钩子; 需要提交前联动检查时执行 `pair init --git-hooks`")
			} else if err := installHooks(ctx, r.Git, &t); err != nil {
				return err
			}
			t.line("")
			t.WriteString(migrationGuide)
			_, err = io.WriteString(cmd.OutOrStdout(), t.String())
			return err
		},
	}
	cmd.Flags().BoolVar(&hooks, "git-hooks", false, "安装 prepare-commit-msg 与 commit-msg 钩子")
	return cmd
}

// writeNew 在文件不存在时写入, 报告是否新建.
func writeNew(path string, data []byte) (bool, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return false, err
	}
	return true, f.Close()
}

// ensureLine 确保文件中有内容恰为 line 的一行, 文件不存在时创建.
func ensureLine(path, line string) error {
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if slices.Contains(strings.Split(string(b), "\n"), line) {
		return nil
	}
	if len(b) > 0 && !bytes.HasSuffix(b, []byte("\n")) {
		b = append(b, '\n')
	}
	return os.WriteFile(path, append(b, line+"\n"...), 0o644)
}

// installHooks 写入 Pairwise 的 Git 钩子. 已存在的 Pairwise 钩子按当前内容重写, 其他钩子不覆盖.
func installHooks(ctx context.Context, g *gitx.Repo, t *text) error {
	dir, err := g.HooksDir(ctx)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var installed []string
	for _, name := range gitHooks {
		path := filepath.Join(dir, name)
		b, err := os.ReadFile(path)
		switch {
		case err == nil && !strings.Contains(string(b), "pair git-hook "+name):
			t.line("- 未覆盖已存在的非 Pairwise 钩子 %s, 请在其中加入一行 `pair git-hook %s \"$@\" || exit $?`", path, name)
			continue
		case err != nil && !errors.Is(err, fs.ErrNotExist):
			return err
		}
		if err := os.WriteFile(path, fmt.Appendf(nil, hookScript, name), 0o755); err != nil {
			return err
		}
		installed = append(installed, name)
	}
	if len(installed) > 0 {
		t.line("- 已安装 Git 钩子 %s (%s)", strings.Join(installed, ", "), dir)
	}
	return nil
}

// load 打开当前目录所在工作树并加载 .pair/ 全部文档.
func load(ctx context.Context) (*repo.Repo, *repo.Index, error) {
	dir, err := os.Getwd()
	if err != nil {
		return nil, nil, err
	}
	r, err := repo.Open(ctx, dir)
	if err != nil {
		return nil, nil, err
	}
	ix, err := r.Load()
	return r, ix, err
}

func (a *app) checkCmd() *cobra.Command {
	var strict, diff bool
	cmd := &cobra.Command{
		Use:   "check [--strict] [--diff [<base-ref>]]",
		Short: "语法与引用校验; --diff 另外检查改动源码对应的模块文档是否同步更新",
		Long: "语法与引用校验. --strict 把警告视为错误, 并在存在待确认问题时报错.\n" +
			"--diff 不带 <base-ref> 时检查暂存区, 带 <base-ref> 时检查 <base-ref>...HEAD (供 CI 使用).",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 && !diff {
				return usagef("<base-ref> 只能与 --diff 一起使用")
			}
			ctx := cmd.Context()
			r, ix, err := load(ctx)
			if err != nil {
				return err
			}
			var diags []*pairv1.Diagnostic
			for _, d := range check.Run(ix, check.Options{Strict: strict}) {
				diags = append(diags, diagProto(d))
			}
			var res *check.DiffResult
			if diff {
				opts := check.DiffOptions{}
				if len(args) > 0 {
					opts.BaseRef = args[0]
				}
				if res, err = check.Diff(ctx, r, ix, opts); err != nil {
					return err
				}
				if !res.OK() {
					diags = append(diags, missingDiags(res.Missing, opts.BaseRef)...)
				}
			}
			if err := a.reply(cmd, &pairv1.CheckResponse{Diagnostics: diags}, checkText(diags, res)); err != nil {
				return err
			}
			if slices.ContainsFunc(diags, func(d *pairv1.Diagnostic) bool { return d.Severity == pairv1.Severity_SEVERITY_ERROR }) {
				return exitCode(1)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&strict, "strict", false, "警告视为错误, 存在待确认问题时报错")
	cmd.Flags().BoolVar(&diff, "diff", false, "检查改动源码命中 scope 的模块文档是否同步更新")
	return cmd
}

func diagProto(d check.Diagnostic) *pairv1.Diagnostic {
	sev := pairv1.Severity_SEVERITY_ERROR
	if d.Severity == doc.SeverityWarning {
		sev = pairv1.Severity_SEVERITY_WARNING
	}
	return &pairv1.Diagnostic{Severity: sev, Path: d.Path, Line: int32(d.Line), Code: d.Code, Message: d.Message, Fix: d.Fix}
}

// missingDiags 把漏更新的模块转成诊断. baseRef 为空表示暂存区模式.
func missingDiags(ms []check.Missing, baseRef string) []*pairv1.Diagnostic {
	skip := "确认不涉及规格变更时执行 `pair skip-update --reason \"<原因>\"` 由 prepare-commit-msg 钩子追加 trailer, " +
		"或在提交信息末尾手写 `" + check.SkipTrailer + ": <原因>`"
	if baseRef != "" {
		skip = "确认不涉及规格变更时在区间内任一提交信息末尾加上 `" + check.SkipTrailer + ": <原因>` trailer"
	}
	var out []*pairv1.Diagnostic
	for _, m := range ms {
		out = append(out, &pairv1.Diagnostic{
			Severity: pairv1.Severity_SEVERITY_ERROR,
			Path:     m.Path,
			Code:     "doc-not-updated",
			Message:  fmt.Sprintf("模块「%s」scope 下的源码有改动, 但模块文档未更新: %s", m.DocID, strings.Join(m.Files, ", ")),
			Fix: fmt.Sprintf("执行 `pair show %s` 核对规则, 有变更时用 `pair update %[1]s#<sec> --base <hash>` 同步并提交 %s; %s",
				m.DocID, m.Path, skip),
		})
	}
	return out
}

func (a *app) mergeDriverCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "merge-driver <base> <ours> <theirs>",
		Short: "供 Git merge/rebase 调用的结构化三路合并驱动, 结果写回 <ours>",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			var docs [3]*doc.Document
			for i, path := range args {
				raw, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				// 没有共同祖先 (双方各自新建) 时 Git 传入空的 base.
				if i == 0 && len(raw) == 0 {
					continue
				}
				if docs[i], err = doc.Parse(raw, doc.KindModule); err != nil {
					return fmt.Errorf("无法按结构合并 (%s): %w; 请手动解决该文件的冲突", path, err)
				}
			}
			res, err := merge.Merge3(docs[0], docs[1], docs[2], merge.Labels{Base: "base", Ours: "ours", Theirs: "theirs"})
			if err != nil {
				return fmt.Errorf("无法按结构合并: %w", err)
			}
			if err := os.WriteFile(args[1], res.Raw, 0o644); err != nil {
				return err
			}
			if len(res.Conflicts) == 0 {
				return nil
			}
			var where []string
			for _, c := range res.Conflicts {
				where = append(where, c.Where)
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "pair merge-driver: %d 处冲突 (%s), 已写入冲突标记\n", len(where), strings.Join(where, ", "))
			return exitCode(1)
		},
	}
}

func (a *app) gitHookCmd() *cobra.Command {
	return group("git-hook", "Git 提交钩子: 追加 Pair-Skip trailer 与提交前联动检查",
		&cobra.Command{
			Use:   "prepare-commit-msg <msgfile> [<source> [<sha>]]",
			Short: "把已登记的跳过原因以 Pair-Skip trailer 追加到提交信息",
			Args:  cobra.RangeArgs(1, 3),
			RunE: func(cmd *cobra.Command, args []string) error {
				err := prepareCommitMsg(cmd.Context(), args[0], len(args) == 1)
				// 钩子不能因 Pairwise 不可用而阻塞提交, commit-msg 的检查仍会兜底.
				if err != nil && !errors.Is(err, repo.ErrNotInitialized) {
					fmt.Fprintf(cmd.ErrOrStderr(), "pair: 未能追加 %s trailer: %s\n", check.SkipTrailer, errMessage(err))
				}
				return nil
			},
		},
		&cobra.Command{
			Use:   "commit-msg <msgfile>",
			Short: "检查暂存区改动源码对应的模块文档是否同步更新",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return commitMsg(cmd, args[0])
			},
		},
	)
}

// prepareCommitMsg 追加常驻进程中登记的跳过原因. editor 表示没有 -m/-F/-t 等来源, 提交信息待在编辑器中填写.
func prepareCommitMsg(ctx context.Context, msgfile string, editor bool) error {
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	r, err := repo.Open(ctx, dir)
	if err != nil {
		return err
	}
	c, err := client.Dial(ctx, r.Root())
	if err != nil {
		return err
	}
	res, err := c.PendingSkips(ctx, &pairv1.PendingSkipsRequest{})
	if err != nil || len(res.Reasons) == 0 {
		return err
	}
	return appendSkips(ctx, r.Git, msgfile, res.Reasons, editor)
}

// appendSkips 用 git interpret-trailers 把跳过原因以 Pair-Skip trailer 追加到提交信息, 相同 trailer 不重复.
// 编辑器模式下提交信息为空, git 只在 trailer 前留一个空行, 填写的标题会与 trailer 紧邻而使其失效;
// 因此补一个空行, 与 git commit -s 生成的模板一致.
func appendSkips(ctx context.Context, g *gitx.Repo, msgfile string, reasons []string, editor bool) error {
	// gitx 在工作树根目录执行 git, 路径须是绝对路径.
	path, err := filepath.Abs(msgfile)
	if err != nil {
		return err
	}
	args := []string{"interpret-trailers", "--in-place", "--where", "end", "--if-exists", "addIfDifferent", "--if-missing", "add"}
	for _, r := range reasons {
		args = append(args, "--trailer", check.SkipTrailer+": "+r)
	}
	if _, err := g.Run(ctx, append(args, path)...); err != nil {
		return err
	}
	if !editor {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil || !bytes.HasPrefix(b, []byte("\n")) || bytes.HasPrefix(b, []byte("\n\n")) {
		return err
	}
	return os.WriteFile(path, append([]byte("\n"), b...), 0o644)
}

// commitMsg 在进程内检查暂存区, 通过后清除已写入提交信息的跳过原因.
// 当前分支没有 .pair/ 时直接放行: 钩子目录跨分支共享, 甚至可能经 core.hooksPath 跨仓库共享.
func commitMsg(cmd *cobra.Command, msgfile string) error {
	ctx := cmd.Context()
	r, ix, err := load(ctx)
	if errors.Is(err, repo.ErrNotInitialized) {
		return nil
	}
	if err != nil {
		return err
	}
	msg, err := os.ReadFile(msgfile)
	if err != nil {
		return err
	}
	res, err := check.Diff(ctx, r, ix, check.DiffOptions{Message: string(msg)})
	if err != nil {
		return err
	}
	if !res.OK() {
		var t text
		t.line("提交被拒绝: 以下模块 scope 下的源码有改动, 但模块文档未同步更新.")
		for _, d := range missingDiags(res.Missing, "") {
			t.diag(d)
		}
		fmt.Fprint(cmd.ErrOrStderr(), t.String())
		return exitCode(1)
	}
	// 常驻进程不可用时不阻塞提交, 未清除的原因留待下次提交时再处理.
	if c, err := client.Dial(ctx, r.Root()); err == nil {
		_, _ = c.ConsumeSkips(ctx, &pairv1.ConsumeSkipsRequest{})
	}
	return nil
}
