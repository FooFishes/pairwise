// Package client 是 CLI 侧对常驻进程的访问: 发现或拉起当前工作树的常驻进程,
// 经 Unix Socket 调用 Connect 接口并订阅 SSE 事件流.
package client

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/gofrs/flock"
	"google.golang.org/protobuf/encoding/protojson"

	pairv1 "github.com/FooFishes/pairwise/gen/pair/v1"
	"github.com/FooFishes/pairwise/gen/pair/v1/pairv1connect"
	"github.com/FooFishes/pairwise/internal/daemon"
	"github.com/FooFishes/pairwise/internal/gitx"
)

// baseURL 只用于构造请求, 连接实际经 Unix Socket 建立.
const baseURL = "http://pair"

const (
	pingTimeout = time.Second
	// waitTimeout 是等待常驻进程启动或退出的上限.
	waitTimeout = 5 * time.Second
	minBackoff  = 100 * time.Millisecond
	maxBackoff  = 5 * time.Second
)

// repoLocalEnv 是 `git rev-parse --local-env-vars` 列出的仓库局部变量. 从 Git 钩子中拉起常驻进程时
// 会继承它们, 而 GIT_INDEX_FILE 等只对本次提交有效, 常驻进程必须按工作目录重新定位仓库.
var repoLocalEnv = []string{
	"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_CONFIG", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT",
	"GIT_OBJECT_DIRECTORY", "GIT_DIR", "GIT_WORK_TREE", "GIT_IMPLICIT_WORK_TREE", "GIT_GRAFT_FILE",
	"GIT_INDEX_FILE", "GIT_NO_REPLACE_OBJECTS", "GIT_REPLACE_REF_BASE", "GIT_PREFIX", "GIT_SHALLOW_FILE",
	"GIT_COMMON_DIR",
}

// Client 是到常驻进程的连接.
type Client struct {
	pairv1connect.PairServiceClient
	// Root 是工作树根目录.
	Root   string
	Socket string

	stateDir string
	http     *http.Client
}

// open 定位 dir 所在工作树并构造客户端, 不检查常驻进程是否在运行.
func open(ctx context.Context, dir string) (*Client, error) {
	r, err := gitx.Open(ctx, dir)
	if err != nil {
		return nil, err
	}
	return newClient(r.Root, filepath.Join(r.GitDir, "pair")), nil
}

func newClient(root, stateDir string) *Client {
	sock := daemon.SocketPath(root)
	// 不设整体超时: ask --wait 长轮询与 SSE 都是长连接.
	hc := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
	}}
	return &Client{
		PairServiceClient: pairv1connect.NewPairServiceClient(connect.NewClient(connecthttp.NewTransport(hc, baseURL))),
		Root:              root,
		Socket:            sock,
		stateDir:          stateDir,
		http:              hc,
	}
}

func (c *Client) ping(ctx context.Context) (*pairv1.PingResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	return c.Ping(ctx, &pairv1.PingRequest{})
}

// Dial 连接 dir 所在工作树的常驻进程. 未运行时以 `pair daemon` 在后台拉起,
// 构建指纹与当前可执行文件不一致时先停止旧进程再拉起.
func Dial(ctx context.Context, dir string) (*Client, error) {
	c, err := open(ctx, dir)
	if err != nil {
		return nil, err
	}
	if res, err := c.ping(ctx); err == nil {
		if res.Fingerprint == daemon.Fingerprint() {
			return c, nil
		}
		if err := c.stop(ctx); err != nil {
			return nil, err
		}
	}
	if err := c.spawn(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

// spawn 以脱离终端的子进程拉起 `pair daemon` 并等待它可用. 多个 CLI 同时拉起时,
// 后到的实例因文件锁退出, 轮询会连上先到的实例.
func (c *Client) spawn(ctx context.Context) error {
	exe, err := executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.stateDir, 0o755); err != nil {
		return err
	}
	logPath := filepath.Join(c.stateDir, "daemon.log")
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer log.Close()
	fi, err := log.Stat()
	if err != nil {
		return err
	}
	// 不用 CommandContext: ctx 结束不应杀死常驻进程.
	cmd := exec.Command(exe, "daemon")
	cmd.Dir = c.Root
	cmd.Env = slices.DeleteFunc(os.Environ(), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return slices.Contains(repoLocalEnv, name)
	})
	cmd.Stdout, cmd.Stderr = log, log
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	exited := make(chan struct{})
	go func() {
		cmd.Wait()
		close(exited)
	}()

	var up bool
	waitUntil(ctx, func() bool {
		if _, err := c.ping(ctx); err == nil {
			up = true
			return true
		}
		select {
		case <-exited:
			// 子进程已退出且没有其他实例持有锁, 说明启动失败而非让位给并发拉起的实例, 不必等满.
			return !c.locked()
		default:
			return false
		}
	})
	if !up {
		return fmt.Errorf("常驻进程未能启动, %s 末尾:\n%s", logPath, logTail(logPath, fi.Size()))
	}
	return nil
}

// locked 报告是否有常驻进程持有工作树的文件锁.
func (c *Client) locked() bool {
	lock := flock.New(daemon.LockPath(c.stateDir))
	ok, _ := lock.TryLock()
	if ok {
		lock.Unlock()
	}
	return !ok
}

// executable 返回拉起常驻进程用的可执行文件路径. 优先沿用调用时的名字而非解析后的真实路径,
// testscript 等按 os.Args[0] 文件名分派命令的场景依赖这一点.
func executable() (string, error) {
	if exe, err := exec.LookPath(os.Args[0]); err == nil {
		return filepath.Abs(exe)
	}
	return os.Executable()
}

// logTail 返回日志自 offset 起新写入内容的最后几行.
func logTail(path string, offset int64) string {
	data, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSpace(string(data[min(offset, int64(len(data))):])), "\n")
	return strings.Join(lines[max(0, len(lines)-10):], "\n")
}

// waitUntil 每 50ms 检查一次 cond, 在 waitTimeout 内成立时返回 true.
func waitUntil(ctx context.Context, cond func() bool) bool {
	ctx, cancel := context.WithTimeout(ctx, waitTimeout)
	defer cancel()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for !cond() {
		select {
		case <-ctx.Done():
			return false
		case <-tick.C:
		}
	}
	return true
}

// Running 报告 dir 所在工作树的常驻进程是否在运行, 不会拉起.
func Running(ctx context.Context, dir string) (*Client, bool) {
	c, err := open(ctx, dir)
	if err != nil {
		return nil, false
	}
	if _, err := c.ping(ctx); err != nil {
		return nil, false
	}
	return c, true
}

// Events 订阅 SSE 事件流, 从序号 since 之后开始, 断线时凭最后的序号自动重连.
// ctx 结束后两个通道都关闭.
func (c *Client) Events(ctx context.Context, since int64) (<-chan *pairv1.Event, <-chan error) {
	events := make(chan *pairv1.Event)
	// 断线错误只保留最近一条, 调用方不读错误通道时不阻塞事件投递.
	errs := make(chan error, 1)
	go func() {
		defer close(errs)
		defer close(events)
		backoff := minBackoff
		for {
			prev := since
			err := c.stream(ctx, &since, events)
			if ctx.Err() != nil {
				return
			}
			if since > prev {
				backoff = minBackoff
			}
			select {
			case errs <- err:
			default:
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, maxBackoff)
		}
	}()
	return events, errs
}

// stream 读取一次 SSE 连接直到断开, 每投递一条事件就推进 since.
// 服务端每条事件只有一行 data, 因此按行解析即可.
func (c *Client) stream(ctx context.Context, since *int64, out chan<- *pairv1.Event) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+daemon.EventsPath, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Last-Event-ID", strconv.FormatInt(*since, 10))
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("订阅事件流失败: %s", res.Status)
	}
	sc := bufio.NewScanner(res.Body)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		ev := new(pairv1.Event)
		if err := protojson.Unmarshal([]byte(data), ev); err != nil {
			return err
		}
		select {
		case out <- ev:
			*since = ev.Seq
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return cmp.Or(sc.Err(), errors.New("事件流已断开"))
}

// UIURL 申请一次性访问令牌并返回带令牌的界面地址.
func (c *Client) UIURL(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+daemon.TokenPath, nil)
	if err != nil {
		return "", err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("申请界面访问令牌失败: %s", res.Status)
	}
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return "", err
	}
	return body.URL, nil
}

// Stop 停止 dir 所在工作树的常驻进程, 未运行时直接返回.
func Stop(ctx context.Context, dir string) error {
	c, ok := Running(ctx, dir)
	if !ok {
		return nil
	}
	return c.stop(ctx)
}

// stop 请求常驻进程退出并等待它释放文件锁. socket 在关闭 HTTP 服务时就已不可连接,
// 但锁要到业务实现关闭后才释放, 此前拉起的新实例会因锁被占用而退出.
func (c *Client) stop(ctx context.Context) error {
	if _, err := c.Shutdown(ctx, &pairv1.ShutdownRequest{}); err != nil {
		return err
	}
	if !waitUntil(ctx, func() bool { return !c.locked() }) {
		return fmt.Errorf("常驻进程未能在 %s 内退出", waitTimeout)
	}
	return nil
}
