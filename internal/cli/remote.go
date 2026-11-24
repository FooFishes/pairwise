package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/cli/browser"
	"github.com/spf13/cobra"

	pairv1 "github.com/FooFishes/pairwise/gen/pair/v1"
	"github.com/FooFishes/pairwise/gen/pair/v1/pairv1connect"
	"github.com/FooFishes/pairwise/internal/client"
	"github.com/FooFishes/pairwise/internal/daemon"
	"github.com/FooFishes/pairwise/internal/repo"
)

// 本文件中的命令经 Unix Socket 调用常驻进程, 未运行时自动拉起.

type svc = pairv1connect.PairServiceClient

func (a *app) serveCmd() *cobra.Command {
	var open, stop bool
	cmd := &cobra.Command{
		Use:   "serve [--open] [--stop]",
		Short: "启动或连接当前工作树的常驻进程并输出界面地址",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, out := cmd.Context(), cmd.OutOrStdout()
			dir, err := os.Getwd()
			if err != nil {
				return err
			}
			if stop {
				if _, ok := client.Running(ctx, dir); !ok {
					_, err := io.WriteString(out, "常驻进程未在运行\n")
					return err
				}
				if err := client.Stop(ctx, dir); err != nil {
					return err
				}
				_, err := io.WriteString(out, "已停止常驻进程\n")
				return err
			}
			c, err := client.Dial(ctx, dir)
			if err != nil {
				return err
			}
			ping, err := c.Ping(ctx, &pairv1.PingRequest{})
			if err != nil {
				return err
			}
			url, err := c.UIURL(ctx)
			if err != nil {
				return err
			}
			var t text
			t.line("常驻进程运行中: pid %d, 版本 %s, 工作树 %s (分支 %s)", ping.Pid, ping.Version, ping.Root, ping.Branch)
			t.line("界面地址: %s", url)
			t.line("下一步: 在浏览器中打开该地址; 地址带一次性令牌, 首次打开后作废, 需要新地址时重新执行 `pair serve`")
			if _, err := io.WriteString(out, t.String()); err != nil || !open {
				return err
			}
			if err := browser.OpenURL(url); err != nil {
				return fmt.Errorf("打开浏览器失败: %w; 请手动打开上面的地址", err)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&open, "open", false, "用系统浏览器打开界面")
	cmd.Flags().BoolVar(&stop, "stop", false, "停止当前工作树的常驻进程")
	cmd.MarkFlagsMutuallyExclusive("open", "stop")
	return cmd
}

func (a *app) statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "查看文档概况、未决问题、待审阅区块与反馈队列",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return rpc(a, cmd, svc.Status, &pairv1.StatusRequest{}, statusText)
		},
	}
}

var listKinds = map[string]pairv1.DocKind{
	"modules":   pairv1.DocKind_DOC_KIND_MODULE,
	"flows":     pairv1.DocKind_DOC_KIND_FLOW,
	"decisions": pairv1.DocKind_DOC_KIND_DECISION,
}

func (a *app) listCmd() *cobra.Command {
	return &cobra.Command{
		Use:       "list [modules|flows|decisions]",
		Short:     "列出模块、流程与待确认问题",
		ValidArgs: []string{"modules", "flows", "decisions"},
		Args:      cobra.MatchAll(cobra.MaximumNArgs(1), cobra.OnlyValidArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			var kind pairv1.DocKind
			if len(args) > 0 {
				kind = listKinds[args[0]]
			}
			return rpc(a, cmd, svc.ListDocs, &pairv1.ListDocsRequest{Kind: kind}, func(res *pairv1.ListDocsResponse) string {
				return listText(res, kind)
			})
		},
	}
}

func (a *app) showCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <id[#sec]>",
		Short: "查看文档或区块内容, 末尾附带整文件 base 哈希与更新提示",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return rpc(a, cmd, svc.Show, &pairv1.ShowRequest{Target: args[0], SessionId: a.session}, showText)
		},
	}
}

func (a *app) lookupCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "lookup <path...>",
		Short: "根据源码路径反查关联的模块文档",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			paths := make([]string, len(args))
			for i, p := range args {
				abs, err := filepath.Abs(p)
				if err != nil {
					return err
				}
				paths[i] = abs
			}
			return rpc(a, cmd, svc.Lookup, &pairv1.LookupRequest{Paths: paths}, lookupText)
		},
	}
}

func (a *app) askCmd() *cobra.Command {
	var (
		file    string
		wait    bool
		timeout int32
	)
	cmd := &cobra.Command{
		Use:   "ask <id> --file <doc.md> [--wait [--timeout <s>]]",
		Short: "提交待确认问题, 可等待开发者确认 (无 --file 时读取 stdin)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("timeout") && !wait {
				return usagef("--timeout 只能与 --wait 一起使用")
			}
			content, err := readInput(cmd, file)
			if err != nil {
				return err
			}
			c, err := dial(cmd)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			res, err := c.Ask(ctx, &pairv1.AskRequest{Id: args[0], Content: content, SessionId: a.session})
			if err != nil {
				return err
			}
			if err := a.reply(cmd, res, askText(res, wait)); err != nil || !wait {
				return err
			}
			w, err := c.WaitDecision(ctx, &pairv1.WaitDecisionRequest{Id: args[0], TimeoutSecs: timeout, SessionId: a.session})
			if err != nil {
				return err
			}
			return a.reply(cmd, w, waitText(w))
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "问题文档路径")
	cmd.Flags().BoolVar(&wait, "wait", false, "等待开发者确认, 超时后按 default_option 继续")
	cmd.Flags().Int32Var(&timeout, "timeout", 0, "等待秒数, 默认取 config.toml 的 ask_wait_timeout_secs")
	return cmd
}

func (a *app) updateCmd() *cobra.Command {
	var (
		file, base, noChange, kind string
		resolves                   []string
	)
	cmd := &cobra.Command{
		Use:   "update <id[#sec]> [--file <doc.md>] [--base <hash>] [--resolves <id>[,<id>...]] [--no-change <reason>] [--kind module|flow]",
		Short: "三路合并更新文档并原子闭环指定反馈 (无 --file 时读取 stdin)",
		Long: "update <id> 的输入是含 Frontmatter 的完整文档; update <id#sec> 的输入是该区块,\n" +
			"首行为 `## 标题 <!-- #sec -->` 时整体替换区块, 否则保留原标题只替换正文, 可以以只含 assumptions 的 Frontmatter 开头整体设置假设列表.\n" +
			"更新已存在的文档须带 `pair show` 输出的 --base; 目标无需改动时用 --resolves <id> --no-change <原因> 显式结案.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if kind != "module" && kind != "flow" {
				return usagef("--kind 只能是 module 或 flow")
			}
			req := &pairv1.UpdateRequest{Target: args[0], Base: base, Resolves: resolves, NoChange: noChange, Kind: kind, SessionId: a.session}
			switch {
			case noChange != "" && file != "":
				return usagef("--no-change 不写入正文, 不能与 --file 同时使用")
			case noChange == "":
				var err error
				if req.Content, err = readInput(cmd, file); err != nil {
					return err
				}
			}
			return rpc(a, cmd, svc.Update, req, func(res *pairv1.UpdateResponse) string { return updateText(req, res) })
		},
	}
	f := cmd.Flags()
	f.StringVarP(&file, "file", "f", "", "输入文件路径")
	f.StringVar(&base, "base", "", "`pair show` 或 `pair bind` 输出的整文件 base 哈希")
	f.StringSliceVar(&resolves, "resolves", nil, "本次更新闭环的反馈 ID, 可重复或用逗号分隔")
	f.StringVar(&noChange, "no-change", "", "目标无需改动时的原因, 不写入正文")
	f.StringVar(&kind, "kind", "module", "新建文档的类型: module 或 flow")
	return cmd
}

func (a *app) drainCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "drain",
		Short: "读取已发送的开发者反馈及其反馈 ID",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return rpc(a, cmd, svc.Drain, &pairv1.DrainRequest{SessionId: a.session}, drainText)
		},
	}
}

func (a *app) stageCmd() *cobra.Command {
	var pick, note string
	decide := &cobra.Command{
		Use:   "decide <id> --pick <opt> [--note <text>]",
		Short: "暂存对待确认问题的选择",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := &pairv1.StageDecideRequest{DecisionId: args[0], Pick: pick, Note: note}
			return rpc(a, cmd, svc.StageDecide, req, func(res *pairv1.StageDecideResponse) string { return stagedText(res.Item) })
		},
	}
	decide.Flags().StringVar(&pick, "pick", "", "所选方案 ID")
	decide.Flags().StringVar(&note, "note", "", "附带的说明")
	_ = decide.MarkFlagRequired("pick")

	var assumption string
	noteCmd := &cobra.Command{
		Use:   "note <id#sec> [--assumption <aid>] <msg>",
		Short: "针对区块或其中某条假设暂存修改说明",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := &pairv1.StageNoteRequest{Target: args[0], AssumptionId: assumption, Message: strings.Join(args[1:], " ")}
			return rpc(a, cmd, svc.StageNote, req, func(res *pairv1.StageNoteResponse) string { return stagedText(res.Item) })
		},
	}
	noteCmd.Flags().StringVar(&assumption, "assumption", "", "针对的假设 ID")

	return group("stage", "开发者决策与反馈暂存区",
		decide,
		noteCmd,
		&cobra.Command{
			Use:   "confirm <id[#sec]>",
			Short: "确认区块: 写入 confirmed_hash 并清空假设与撤回假设, 省略 #sec 时确认全部区块",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return rpc(a, cmd, svc.Confirm, &pairv1.ConfirmRequest{Target: args[0]}, confirmText)
			},
		},
		&cobra.Command{
			Use:   "list",
			Short: "列出当前分支暂存与未闭环的反馈",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return rpc(a, cmd, svc.ListFeedback, &pairv1.ListFeedbackRequest{}, feedbackListText)
			},
		},
		&cobra.Command{
			Use:   "remove <id>",
			Short: "撤销一条尚未发送的暂存条目",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return rpc(a, cmd, svc.StageRemove, &pairv1.StageRemoveRequest{Id: args[0]}, func(*pairv1.StageRemoveResponse) string {
					return fmt.Sprintf("已从暂存区移除 %s\n", args[0])
				})
			},
		},
	)
}

func (a *app) triggerCmd() *cobra.Command {
	var steer bool
	cmd := &cobra.Command{
		Use:   "trigger [--steer]",
		Short: "发送暂存区反馈",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return rpc(a, cmd, svc.Trigger, &pairv1.TriggerRequest{Steer: steer}, triggerText)
		},
	}
	cmd.Flags().BoolVar(&steer, "steer", false, "要求 Agent 立即转向")
	return cmd
}

func (a *app) bindCmd() *cobra.Command {
	var preserve string
	cmd := &cobra.Command{
		Use:   "bind <id[#sec]...> [--preserve <msg>]",
		Short: "关联宿主会话与目标模块区块, 缓存整文件合并基准",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := &pairv1.BindRequest{Targets: args, SessionId: a.session, Preserve: preserve}
			return rpc(a, cmd, svc.Bind, req, func(res *pairv1.BindResponse) string { return bindText(req, res) })
		},
	}
	cmd.Flags().StringVar(&preserve, "preserve", "", "保持规格模式: 声明只修缺陷、调样式或重构, 不改文档")
	return cmd
}

func (a *app) skipUpdateCmd() *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "skip-update --reason <text>",
		Short: "确认本次修改不涉及规格变更, 放行并登记待提交的跳过原因",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			req := &pairv1.SkipUpdateRequest{SessionId: a.session, Reason: reason}
			return rpc(a, cmd, svc.SkipUpdate, req, func(*pairv1.SkipUpdateResponse) string { return skipText(reason) })
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "不更新文档的原因")
	_ = cmd.MarkFlagRequired("reason")
	return cmd
}

func (a *app) watchCmd() *cobra.Command {
	var (
		jsonl bool
		since int64
	)
	cmd := &cobra.Command{
		Use:   "watch --jsonl [--since <seq>]",
		Short: "持续输出状态变更与 Trigger 事件流, 每行一个 JSON 事件",
		Long: "持续输出事件, 断线时凭最后的序号自动重连补发. 默认只输出此后的新事件,\n" +
			"订阅方记录最后处理的 seq 并在重启时用 --since 传入以补发错过的事件. 指定 --session 时跳过属于其他会话的事件.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := dial(cmd)
			if err != nil {
				return err
			}
			if !cmd.Flags().Changed("since") {
				ping, err := c.Ping(cmd.Context(), &pairv1.PingRequest{})
				if err != nil {
					return err
				}
				since = ping.LastSeq
			}
			events, _ := c.Events(cmd.Context(), since)
			for ev := range events {
				if a.session != "" && ev.SessionId != "" && ev.SessionId != a.session {
					continue
				}
				if err := writeJSON(cmd.OutOrStdout(), ev, false); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonl, "jsonl", false, "以 JSON Lines 输出")
	cmd.Flags().Int64Var(&since, "since", 0, "从该序号之后开始输出")
	_ = cmd.MarkFlagRequired("jsonl")
	return cmd
}

func (a *app) sessionCmd() *cobra.Command {
	var capabilities []string
	state := &cobra.Command{
		Use:       "state <idle|running|waiting> [--capabilities <list>]",
		Short:     "上报宿主会话状态与能力",
		ValidArgs: []string{"idle", "running", "waiting"},
		Args:      cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := &pairv1.SetSessionStateRequest{SessionId: a.session, State: args[0], Capabilities: capabilities}
			return rpc(a, cmd, svc.SetSessionState, req, func(*pairv1.SetSessionStateResponse) string {
				return fmt.Sprintf("会话 %s 状态: %s\n", a.session, args[0])
			})
		},
	}
	state.Flags().StringSliceVar(&capabilities, "capabilities", nil, "宿主能力列表, 逗号分隔")
	return group("session", "宿主会话", state)
}

var hookEvents = []string{"session-start", "prompt-submit", "before-tool", "after-tool", "before-stop"}

func (a *app) hookCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "hook <" + strings.Join(hookEvents, "|") + ">",
		Short: "宿主生命周期 Hook: stdin 为宿主 JSON, 输出交回宿主",
		// 退出码 2 在宿主中表示阻塞, 用法问题也只能以 1 结束, 因此参数在执行阶段自行校验, 未知参数忽略.
		FParseErrWhitelist: cobra.FParseErrWhitelist{UnknownFlags: true},
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 || !slices.Contains(hookEvents, args[0]) {
				return fmt.Errorf("pair hook 需要恰好一个事件参数: %s", strings.Join(hookEvents, ", "))
			}
			payload, err := io.ReadAll(cmd.InOrStdin())
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			dir, err := os.Getwd()
			if err != nil {
				return err
			}
			// 不在已初始化的工作树中或常驻进程不可用时静默放行, 不能阻塞宿主.
			if _, err := repo.Open(ctx, dir); err != nil {
				return nil
			}
			c, err := client.Dial(ctx, dir)
			if err != nil {
				return nil
			}
			envFile := os.Getenv("CLAUDE_ENV_FILE")
			res, err := c.Hook(ctx, &pairv1.HookRequest{Event: args[0], Payload: payload, EnvFile: envFile})
			if err != nil {
				return err
			}
			if envFile != "" && res.EnvFileAppend != "" {
				if err := appendFile(envFile, res.EnvFileAppend); err != nil {
					return err
				}
			}
			_, err = cmd.OutOrStdout().Write(res.Output)
			return err
		},
	}
}

func appendFile(path, s string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(s); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func (a *app) daemonCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "daemon",
		Short:  "在前台运行当前工作树的常驻进程",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := os.Getwd()
			if err != nil {
				return err
			}
			return daemon.Run(cmd.Context(), dir, a.newService)
		},
	}
}
