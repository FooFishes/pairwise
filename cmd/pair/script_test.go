package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rogpeppe/go-internal/testscript"

	"github.com/FooFishes/pairwise/internal/cli"
	"github.com/FooFishes/pairwise/internal/core"
)

func TestMain(m *testing.M) {
	testscript.Main(m, map[string]func(){
		"pair": func() { os.Exit(cli.Execute(core.New)) },
	})
}

// TestScript 在临时创建的真实 Git 仓库中执行 testdata/script 下的完整 CLI 流程.
func TestScript(t *testing.T) {
	testscript.Run(t, testscript.Params{
		Dir: filepath.Join("..", "..", "testdata", "script"),
		Setup: func(env *testscript.Env) error {
			// Socket 路径受系统长度上限约束, 不能放在很深的 $WORK 下.
			runtime, err := os.MkdirTemp("", "pair")
			if err != nil {
				return err
			}
			env.Setenv("PAIR_RUNTIME_DIR", runtime)
			env.Setenv("HOME", env.WorkDir)
			env.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			// 关闭提交后在后台运行的 git maintenance, 它会与临时目录的清理竞争.
			env.Setenv("GIT_CONFIG_COUNT", "1")
			env.Setenv("GIT_CONFIG_KEY_0", "maintenance.auto")
			env.Setenv("GIT_CONFIG_VALUE_0", "false")
			env.Setenv("GIT_AUTHOR_NAME", "pair")
			env.Setenv("GIT_AUTHOR_EMAIL", "pair@example.com")
			env.Setenv("GIT_COMMITTER_NAME", "pair")
			env.Setenv("GIT_COMMITTER_EMAIL", "pair@example.com")
			env.Defer(func() {
				// 每个用例结束时停止它拉起的常驻进程.
				stop := exec.Command("pair", "serve", "--stop")
				stop.Dir = env.WorkDir
				stop.Env = env.Vars
				_ = stop.Run()
				_ = os.RemoveAll(runtime)
			})
			return nil
		},
		Cmds: map[string]func(ts *testscript.TestScript, neg bool, args []string){
			"exitcode": cmdExitCode,
			"expand":   cmdExpand,
			"grab":     cmdGrab,
			"http":     cmdHTTP,
			"poll":     cmdPoll,
			"sleep":    cmdSleep,
		},
	})
}

// cmdExitCode 实现 `exitcode CODE PROGRAM [ARGS...]`: 执行程序并要求退出码恰为 CODE.
// 宿主把 Hook 的退出码 2 视为阻塞, `! pair hook` 无法区分它与 1.
func cmdExitCode(ts *testscript.TestScript, neg bool, args []string) {
	if neg || len(args) < 2 {
		ts.Fatalf("usage: exitcode CODE PROGRAM [ARGS...]")
	}
	want, err := strconv.Atoi(args[0])
	ts.Check(err)
	code := 0
	var exit *exec.ExitError
	if err := ts.Exec(args[1], args[2:]...); errors.As(err, &exit) {
		code = exit.ExitCode()
	} else {
		ts.Check(err)
	}
	if code != want {
		ts.Fatalf("%s 的退出码为 %d, 期望 %d", strings.Join(args[1:], " "), code, want)
	}
}

// cmdExpand 实现 `expand FILE...`: 原地把 JSON 文件中的 $NAME 展开为按 JSON 字符串转义的环境变量值.
// 宿主 Hook 输入中的 cwd 与 file_path 是绝对路径, 归档中的 JSON 须在运行时代入 $WORK (Windows 上含反斜杠).
func cmdExpand(ts *testscript.TestScript, neg bool, args []string) {
	if neg || len(args) == 0 {
		ts.Fatalf("usage: expand FILE...")
	}
	escape := func(name string) string {
		b, err := json.Marshal(ts.Getenv(name))
		ts.Check(err)
		return string(b[1 : len(b)-1])
	}
	for _, f := range args {
		ts.Check(os.WriteFile(ts.MkAbs(f), []byte(os.Expand(ts.ReadFile(f), escape)), 0o644))
	}
}

// cmdGrab 实现 `grab NAME REGEXP FILE`: 把 FILE (可以是 stdout 或 stderr) 中 REGEXP 首个分组的
// 首次匹配存入环境变量 NAME, 供后续命令以 $NAME 引用 (如 base 哈希、端口与令牌).
func cmdGrab(ts *testscript.TestScript, neg bool, args []string) {
	if neg || len(args) != 3 {
		ts.Fatalf("usage: grab NAME REGEXP FILE")
	}
	m := regexp.MustCompile(args[1]).FindStringSubmatch(ts.ReadFile(args[2]))
	if len(m) < 2 {
		ts.Fatalf("%s 中没有匹配 %q 的内容", args[2], args[1])
	}
	ts.Setenv(args[0], m[1])
}

// cmdHTTP 实现 `http [-H 'Name: value']... METHOD URL [BODY]`: 不跟随重定向地发出请求,
// 向 stdout 输出 `status <code>`、Location 与 Set-Cookie 响应头以及响应体. `-H 'Host: x'` 改写 Host.
func cmdHTTP(ts *testscript.TestScript, neg bool, args []string) {
	var headers []string
	for len(args) >= 2 && args[0] == "-H" {
		headers, args = append(headers, args[1]), args[2:]
	}
	if len(args) != 2 && len(args) != 3 {
		ts.Fatalf("usage: http [-H 'Name: value']... METHOD URL [BODY]")
	}
	var body io.Reader
	if len(args) == 3 {
		body = strings.NewReader(args[2])
	}
	req, err := http.NewRequest(args[0], args[1], body)
	ts.Check(err)
	for _, h := range headers {
		k, v, _ := strings.Cut(h, ":")
		if v = strings.TrimSpace(v); strings.EqualFold(k, "Host") {
			req.Host = v
		} else {
			req.Header.Set(k, v)
		}
	}
	c := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := c.Do(req)
	ts.Check(err)
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	ts.Check(err)
	fmt.Fprintf(ts.Stdout(), "status %d\n", res.StatusCode)
	for _, k := range []string{"Location", "Set-Cookie"} {
		for _, v := range res.Header.Values(k) {
			fmt.Fprintf(ts.Stdout(), "%s: %s\n", k, v)
		}
	}
	fmt.Fprintf(ts.Stdout(), "\n%s", b)
	if ok := res.StatusCode < 400; ok == neg {
		ts.Fatalf("unexpected status %s", res.Status)
	}
}

// cmdPoll 实现 `poll REGEXP PROGRAM [ARGS...]`: 每 50ms 执行一次程序, 直到其 stdout 匹配 REGEXP,
// 10 秒内仍不匹配则失败. 用于等待常驻进程经文件监听感知分支切换或外部改动.
func cmdPoll(ts *testscript.TestScript, neg bool, args []string) {
	if neg || len(args) < 2 {
		ts.Fatalf("usage: poll REGEXP PROGRAM [ARGS...]")
	}
	re := regexp.MustCompile(args[0])
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if ts.Exec(args[1], args[2:]...) == nil && re.MatchString(ts.ReadFile("stdout")) {
			return
		}
		if time.Now().After(deadline) {
			ts.Fatalf("10 秒内 %s 的输出仍不匹配 %q", strings.Join(args[1:], " "), args[0])
		}
	}
}

// cmdSleep 实现 `sleep DURATION`, 用于等待后台命令或文件监听生效.
func cmdSleep(ts *testscript.TestScript, neg bool, args []string) {
	if neg || len(args) != 1 {
		ts.Fatalf("usage: sleep DURATION")
	}
	d, err := time.ParseDuration(args[0])
	ts.Check(err)
	time.Sleep(d)
}
