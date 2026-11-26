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
	"github.com/FooFishes/pairwise/internal/doc"
	"github.com/FooFishes/pairwise/internal/gitx"
	"github.com/FooFishes/pairwise/internal/merge"
	"github.com/FooFishes/pairwise/internal/repo"
	"github.com/FooFishes/pairwise/internal/store"
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
   - 模块: ` + "`pair update <id> --kind module <<'EOF'` (或 `--file <doc.md>`)" + `, Frontmatter 含 id 与 scope (关联源码的 Glob 列表).
   - 流程: ` + "`pair update <id> --kind flow <<'EOF'` (或 `--file <doc.md>`)" + `, Frontmatter 只含 id, 正文用 ` + "`@module#sec`" + ` 引用关联区块.
   - 正文按 ` + "`## 标题 <!-- #sec -->`" + ` 切分区块; Agent 自行补全、未经开发者确认的细节逐条写入 sections.<sec>.assumptions (id 与 text), 不写 confirmed_hash.
2. 归拢遗留问题: 业务类代码 TODO 迁入 ` + "`<!-- pw:workaround id=\"...\" -->`" + ` 块并写明解除条件; 旧文档之间或与代码不符之处写成待确认问题 (含 targets、default_option 与 pw:compare 方案对比), 用 ` + "`pair ask <id> <<'EOF'` (或 `--file <doc.md>`)" + ` 提交.
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
			if a.json {
				return usagef("init 只输出面向模型的文本与迁移指引, 不支持 --json")
			}
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
			// 交叉合并时 git 先合并多个合并基得到虚拟 base; 带冲突标记的虚拟 base 无法按结构解析, 内层合并改用 --virtual.
			for _, kv := range [][2]string{
				{"merge.pair-virtual.name", "Pairwise 结构化三路合并 (交叉合并的合并基)"},
				{"merge.pair-virtual.driver", "pair merge-driver --virtual --marker-size %L %O %A %B"},
				{"merge.pair.recursive", "pair-virtual"},
			} {
				if err := r.Git.SetConfig(ctx, kv[0], kv[1]); err != nil {
					return err
				}
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
			rerun := "pair check"
			if strict {
				rerun += " --strict"
			}
			if diff {
				rerun = strings.Join(append([]string{rerun, "--diff"}, args...), " ")
				opts := check.DiffOptions{}
				if len(args) > 0 {
					opts.BaseRef = args[0]
				}
				if res, err = check.Diff(ctx, r, ix, opts); err != nil && opts.BaseRef != "" {
					return fmt.Errorf("无法比较 %s...HEAD: %w\n修复操作: 用 `git rev-parse --verify %[1]s` 确认该引用存在; CI 的浅克隆中先执行 `git fetch origin <分支>` 取回目标分支后重试", opts.BaseRef, err)
				}
				if err != nil {
					return err
				}
				if !res.OK() {
					diags = append(diags, missingDiags(res.Missing, opts.BaseRef)...)
				}
			}
			if err := a.reply(cmd, &pairv1.CheckResponse{Diagnostics: diags}, checkText(diags, res, rerun)); err != nil {
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

// diagProto 转换诊断; doc 包与 proto 的 Severity 枚举取值一一对应.
func diagProto(d check.Diagnostic) *pairv1.Diagnostic {
	return &pairv1.Diagnostic{Severity: pairv1.Severity(d.Severity), Path: d.Path, Line: int32(d.Line), Code: d.Code, Message: d.Message, Fix: d.Fix}
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
	var virtual bool
	var markerSize int
	cmd := &cobra.Command{
		Use:   "merge-driver <base> <ours> <theirs>",
		Short: "供 Git merge/rebase 调用的结构化三路合并驱动, 结果写回 <ours>",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			front, err := readFront(args)
			if err != nil {
				return err
			}
			res, err := mergeDocs(args, virtual)
			switch {
			// 合并基之间只有区块顺序冲突时 Virtual 已按 base 的顺序写出, 不按行合并: 按行合并移动过的区块可能把它复制一份, 外层合并随之删掉它.
			case err == nil && (len(res.Conflicts) == 0 || !virtual || orderOnly(res.Conflicts)):
				if err := os.WriteFile(args[1], res.Raw, 0o644); err != nil {
					return err
				}
				if len(res.Conflicts) == 0 || virtual {
					return nil
				}
				fmt.Fprint(cmd.ErrOrStderr(), conflictText(res.Conflicts, front))
				return exitCode(1)
			case virtual:
				// 合并基之间有冲突或无法解析时按行合并, 与 git 文本合并得到的虚拟 base 一致: 冲突处的标记不等于任何一侧,
				// 长度取 git 给的 %L, 与外层合并的标记区分开; 外层合并见到带标记的 base 就退回按行合并,
				// 不会把与 base 相同的一侧当作未改动而静默采用另一侧.
				_, err := gitx.MergeFile(cmd.Context(), args[1], args[0], args[2], markerSize)
				return err
			}
			// 退回 git 的按行合并, 不能只留下 ours 一侧.
			n, merr := gitx.MergeFile(cmd.Context(), args[1], args[0], args[2], markerSize)
			if merr != nil {
				return merr
			}
			merged, rerr := os.ReadFile(args[1])
			if rerr != nil {
				return rerr
			}
			_, perr := doc.Parse(merged, doc.KindModule)
			switch {
			case perr != nil && (n > 0 || doc.HasMarkers(merged)):
				fmt.Fprintf(cmd.ErrOrStderr(), "pair merge-driver: 无法按结构合并 (%v), 已按行合并, 结果仍无法解析\n%s", err, conflictHint(front))
			case perr != nil:
				fmt.Fprintf(cmd.ErrOrStderr(), "pair merge-driver: 无法按结构合并 (%v), 已按行合并, 结果中没有冲突标记但仍无法解析 (%v)\n"+
					"修复操作: 执行 `pair show %s` 查看当前内容, 按上述原因修正后执行 %s, 再 `git add` 该文件完成合并\n", err, perr, frontID(front), redoCmd(front))
			case n > 0:
				fmt.Fprintf(cmd.ErrOrStderr(), "pair merge-driver: 无法按结构合并 (%v), 已按行合并, %d 处冲突的标记位于围栏代码块内\n%s", err, n, conflictHint(front))
			default:
				fmt.Fprintf(cmd.ErrOrStderr(), "pair merge-driver: 无法按结构合并 (%v), 已按行合并, 结果中没有冲突标记\n"+
					"修复操作: 执行 `pair show %s` 核对合并结果, 有误时按其「下一步」修正, 再 `git add` 该文件完成合并\n", err, frontID(front))
			}
			return exitCode(1)
		},
	}
	cmd.Flags().BoolVar(&virtual, "virtual", false, "合并交叉合并的多个合并基: 有冲突或无法解析时按行合并且不报冲突, 结果作为外层合并的虚拟 base")
	cmd.Flags().IntVar(&markerSize, "marker-size", 7, "按行合并时冲突标记的长度, 由 git 以 %L 传入")
	return cmd
}

// mergeDocs 按结构合并 git 传入的 base、ours 与 theirs 三个文件. 没有共同祖先 (双方各自新建) 时 base 为空文件.
// 交叉合并的虚拟 base 用更长的标记 (git 的 %L), 正文中的这类标记 doc.Parse 不认; 合并基与合并虚拟 base 时的
// 全部输入都按任意长度的标记检查, 带标记的视为无法解析.
func mergeDocs(paths []string, virtual bool) (*merge.Result, error) {
	var docs [3]*doc.Document
	for i, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if i == 0 && len(raw) == 0 {
			continue
		}
		role := []string{"合并基", "ours ", "theirs "}[i]
		if (i == 0 || virtual) && doc.HasMarkers(raw) {
			return nil, fmt.Errorf("%s含未解决的合并冲突", role)
		}
		if docs[i], err = doc.Parse(raw, doc.KindModule); err != nil {
			return nil, fmt.Errorf("%s无法解析: %w", role, err)
		}
	}
	if virtual {
		return merge.Virtual(docs[0], docs[1], docs[2])
	}
	return merge.Merge3(docs[0], docs[1], docs[2], merge.Labels{Base: "base", Ours: "ours", Theirs: "theirs"})
}

// orderOnly 报告冲突是否只有区块顺序一处.
func orderOnly(cs []merge.Conflict) bool {
	return len(cs) == 1 && cs[0].Where == "order"
}

// conflictText 说明按结构合并的冲突与修复操作. 区块顺序冲突不写标记, 按 ours 的顺序写入, 因此列出两侧的顺序.
func conflictText(cs []merge.Conflict, f *doc.Front) string {
	var where []string
	var order *merge.Conflict
	for i, c := range cs {
		where = append(where, c.Where)
		if c.Where == "order" {
			order = &cs[i]
		}
	}
	if orderOnly(cs) {
		return fmt.Sprintf("pair merge-driver: 两侧以不同方式调整了区块顺序, 已按 ours 的顺序写入, 文件中没有冲突标记 (ours: %s; theirs: %s)\n"+
			"修复操作: 需要其他顺序时执行 `pair show %s` 查看当前内容, 调整区块顺序后执行 %s, 再 `git add` 该文件完成合并\n",
			orderText(order.Ours), orderText(order.Theirs), frontID(f), redoCmd(f))
	}
	msg := "已写入冲突标记"
	if order != nil {
		msg += fmt.Sprintf("; 两侧以不同方式调整了区块顺序, 已按 ours 的顺序写入, theirs 的顺序为 %s", orderText(order.Theirs))
	}
	return fmt.Sprintf("pair merge-driver: %d 处冲突 (%s), %s\n%s", len(where), strings.Join(where, ", "), msg, conflictHint(f))
}

// orderText 把按行排列的区块键序写成一行, 前言区的空键不列出.
func orderText(keys []byte) string {
	return strings.Join(slices.DeleteFunc(strings.Split(string(keys), "\n"), func(k string) bool { return k == "" }), ", ")
}

// readFront 依次读取 ours、theirs 与 base 的 Frontmatter, 取第一个带 id 的, 用来给出修复命令; 都不可读时为 nil.
func readFront(paths []string) (*doc.Front, error) {
	for _, i := range []int{1, 2, 0} {
		raw, err := os.ReadFile(paths[i])
		if err != nil {
			return nil, err
		}
		if f, err := doc.ReadFront(raw); err == nil && f.ID != "" {
			return f, nil
		}
	}
	return nil, nil
}

// redoCmd 返回以完整文档覆盖的命令. 驱动参数中没有文件路径, 按 readFront 读到的 Frontmatter 区分问题文档:
// 只有它带 targets 与 default_option, 改用 pair ask 重新提交.
func redoCmd(f *doc.Front) string {
	id := frontID(f)
	if f != nil && (len(f.Targets) > 0 || f.DefaultOption != "") {
		return fmt.Sprintf("`pair ask %s <<'EOF'` (或 `--file <doc.md>`) 重新提交问题", id)
	}
	return fmt.Sprintf("`pair update %s --base <hash> <<'EOF'` (或 `--file <doc.md>`) 以完整文档覆盖", id)
}

func frontID(f *doc.Front) string {
	if f == nil {
		return "<id>"
	}
	return f.ID
}

// conflictHint 返回解决带冲突标记的合并结果的修复操作.
func conflictHint(f *doc.Front) string {
	id, redo := frontID(f), redoCmd(f)
	return fmt.Sprintf("修复操作: 执行 `pair show %s` 查看冲突, 合并两侧内容并删除冲突标记行后执行 %s, "+
		"再 `git add` 该文件完成合并; 不要用 `git checkout --ours/--theirs` 丢弃另一侧\n", id, redo)
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

// prepareCommitMsg 追加运行态中登记的跳过原因. editor 表示没有 -m/-F/-t 等来源, 提交信息待在编辑器中填写.
// 进程内直接读运行态存储 (WAL 支持与常驻进程并发), 不拉起常驻进程.
func prepareCommitMsg(ctx context.Context, msgfile string, editor bool) error {
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	r, err := repo.Open(ctx, dir)
	if err != nil || replaying(r.Git.GitDir) {
		return err
	}
	st, err := store.Open(r.StateDir)
	if err != nil {
		return err
	}
	defer st.Close()
	reasons, err := st.PendingSkips()
	if err != nil || len(reasons) == 0 {
		return err
	}
	return appendSkips(ctx, r.Git, msgfile, reasons, editor)
}

// replaying 报告工作树是否正在 rebase、cherry-pick 或 revert. 重放的提交沿用原提交信息, 待用的跳过原因不属于它们;
// 无冲突的重放也不运行 commit-msg, 追加的原因不会被清除. git am 与 rebase --apply 不运行 prepare-commit-msg.
func replaying(gitDir string) bool {
	for _, name := range []string{"rebase-merge", "sequencer", "CHERRY_PICK_HEAD", "REVERT_HEAD"} {
		if _, err := os.Stat(filepath.Join(gitDir, name)); err == nil {
			return true
		}
	}
	return false
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
	// 在编辑器中放弃填写或 -m "" 时, prepare-commit-msg 追加的 trailer 会使 git 不再按空提交信息中止, 由本钩子代为中止.
	// git 未使用编辑器时以 GIT_EDITOR=: 运行钩子, 此时注释行也属于提交信息.
	if len(res.SkipReasons) > 0 && gitx.EmptyMessage(string(msg), check.SkipTrailer, os.Getenv("GIT_EDITOR") != ":") {
		fmt.Fprint(cmd.ErrOrStderr(), "提交已中止: 提交信息除 Pair-Skip trailer 外为空, 跳过原因保留到下次提交.\n修复操作: 重新执行 `git commit` 并填写提交信息\n")
		return exitCode(1)
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
	// 只清除写入了本次提交信息的跳过原因. 清除失败不阻塞提交, 未清除的原因留待下次提交时再处理.
	if err := consumeSkips(r.StateDir, res.SkipReasons); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "pair: 未能清除已写入提交信息的跳过原因: %s\n", err)
	}
	return nil
}

// consumeSkips 在进程内清除运行态中的 reasons.
func consumeSkips(stateDir string, reasons []string) error {
	if len(reasons) == 0 {
		return nil
	}
	st, err := store.Open(stateDir)
	if err != nil {
		return err
	}
	defer st.Close()
	return st.ConsumeSkips(reasons)
}
