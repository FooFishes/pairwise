// Package cli 定义 pair 命令行. 默认文本输出面向模型阅读: 成功时附带下一步提示,
// 失败时给出原因与可直接执行的修复命令; --json 输出结构化结果.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"connectrpc.com/connect/v2"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/FooFishes/pairwise/gen/pair/v1/pairv1connect"
	"github.com/FooFishes/pairwise/internal/client"
	"github.com/FooFishes/pairwise/internal/daemon"
)

// Execute 运行命令行并返回退出码. newService 用于隐藏子命令 pair daemon 启动常驻进程,
// 由 main 注入以免 cli 依赖 core.
func Execute(newService daemon.NewService) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return run(ctx, newService, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
}

// run 执行一次命令. 退出码: 成功 0, 业务错误与检查未通过 1, 用法错误 2.
func run(ctx context.Context, newService daemon.NewService, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	root := newRoot(newService)
	root.SetArgs(args)
	root.SetIn(stdin)
	root.SetOut(stdout)
	root.SetErr(stderr)
	cmd, err := root.ExecuteContextC(ctx)
	if err == nil {
		return 0
	}
	if code, ok := errors.AsType[exitCode](err); ok {
		return int(code)
	}
	// 执行阶段的业务错误都已被 wrapRun 转成 exitCode, 其余错误来自参数解析与校验.
	fmt.Fprintf(stderr, "用法错误: %s\n用法: %s\n执行 `%s --help` 查看完整说明\n", err, cmd.UseLine(), cmd.CommandPath())
	return 2
}

// app 持有全局参数.
type app struct {
	newService daemon.NewService
	json       bool
	session    string
}

func newRoot(newService daemon.NewService) *cobra.Command {
	a := &app{newService: newService}
	root := &cobra.Command{
		Use:           "pair",
		Short:         "Pairwise: 让文档跟随代码实时演进的仓库原生结对工具",
		Version:       daemon.Version,
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.SetVersionTemplate("{{.Version}}\n")
	// 两个参数对全部命令生效: before-tool Hook 会给任意 pair 命令补上 --session.
	pf := root.PersistentFlags()
	pf.BoolVar(&a.json, "json", false, "输出响应的 JSON")
	pf.StringVar(&a.session, "session", os.Getenv("PAIR_SESSION_ID"), "宿主会话 ID, 默认取环境变量 PAIR_SESSION_ID")
	root.AddCommand(
		a.initCmd(), a.checkCmd(), a.mergeDriverCmd(), a.gitHookCmd(),
		a.serveCmd(), a.statusCmd(), a.listCmd(), a.showCmd(), a.lookupCmd(),
		a.askCmd(), a.updateCmd(), a.drainCmd(), a.stageCmd(), a.triggerCmd(),
		a.bindCmd(), a.skipUpdateCmd(), a.watchCmd(), a.sessionCmd(), a.hookCmd(), a.daemonCmd(),
	)
	wrapRun(root)
	return root
}

// exitCode 是原因已输出、只需以该退出码结束的错误.
type exitCode int

func (c exitCode) Error() string {
	return "exit " + strconv.Itoa(int(c))
}

// usageError 是执行阶段才能发现的用法错误, 与参数解析错误一样以退出码 2 结束.
type usageError string

func (e usageError) Error() string {
	return string(e)
}

func usagef(format string, args ...any) error {
	return usageError(fmt.Sprintf(format, args...))
}

// wrapRun 让执行阶段的错误输出原因后以退出码 1 结束, 从而与参数解析阶段的用法错误区分开.
func wrapRun(c *cobra.Command) {
	if run := c.RunE; run != nil {
		c.RunE = func(cmd *cobra.Command, args []string) error {
			err := run(cmd, args)
			if err == nil || errors.As(err, new(exitCode)) || errors.As(err, new(usageError)) {
				return err
			}
			fmt.Fprintln(cmd.ErrOrStderr(), errMessage(err))
			return exitCode(1)
		}
	}
	for _, sub := range c.Commands() {
		wrapRun(sub)
	}
}

// errMessage 返回面向用户的错误信息. 常驻进程生成的业务错误只取其 Message, 不带错误码前缀.
func errMessage(err error) string {
	if ce, ok := errors.AsType[*connect.Error](err); ok && ce.Message() != "" {
		return ce.Message()
	}
	return err.Error()
}

// group 返回只承载子命令的命令, 缺少子命令或子命令未知时按用法错误处理.
func group(use, short string, subs ...*cobra.Command) *cobra.Command {
	c := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var names []string
			for _, sub := range cmd.Commands() {
				names = append(names, sub.Name())
			}
			return usagef("缺少子命令, 可选: %s", strings.Join(names, ", "))
		},
	}
	c.AddCommand(subs...)
	return c
}

// reply 在 --json 时输出 msg 的 JSON, 否则输出 text.
func (a *app) reply(cmd *cobra.Command, msg proto.Message, text string) error {
	if a.json {
		return writeJSON(cmd.OutOrStdout(), msg, true)
	}
	_, err := io.WriteString(cmd.OutOrStdout(), text)
	return err
}

// writeJSON 输出 msg 的 protojson, indent 为假时输出单行. protojson 有意随机化空白,
// 经 encoding/json 规整后输出才稳定.
func writeJSON(w io.Writer, msg proto.Message, indent bool) error {
	b, err := protojson.Marshal(msg)
	if err != nil {
		return err
	}
	var out bytes.Buffer
	if indent {
		err = json.Indent(&out, b, "", "  ")
	} else {
		err = json.Compact(&out, b)
	}
	if err != nil {
		return err
	}
	out.WriteByte('\n')
	_, err = out.WriteTo(w)
	return err
}

// dial 连接当前目录所在工作树的常驻进程, 未运行时自动拉起.
func dial(cmd *cobra.Command) (*client.Client, error) {
	dir, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return client.Dial(cmd.Context(), dir)
}

// rpc 连接常驻进程调用 method, 按 --json 或 text 输出响应.
func rpc[Req any, Res proto.Message](a *app, cmd *cobra.Command,
	method func(pairv1connect.PairServiceClient, context.Context, Req) (Res, error), req Req, text func(Res) string) error {
	c, err := dial(cmd)
	if err != nil {
		return err
	}
	res, err := method(c, cmd.Context(), req)
	if err != nil {
		return err
	}
	return a.reply(cmd, res, text(res))
}

// readInput 读取 --file 指定的文件; 未指定时读取 stdin, stdin 是终端时按缺少输入的用法错误处理.
func readInput(cmd *cobra.Command, file string) (string, error) {
	if file != "" {
		b, err := os.ReadFile(file)
		return string(b), err
	}
	in := cmd.InOrStdin()
	if f, ok := in.(*os.File); ok {
		if fi, err := f.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
			return "", usagef("缺少输入: 用 --file <doc.md> 指定文件, 或从 stdin 传入内容 (如 `<<'EOF'`)")
		}
	}
	b, err := io.ReadAll(in)
	return string(b), err
}
