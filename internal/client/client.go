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
	"io/fs"
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
	"github.com/FooFishes/pairwise/internal/repo"
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
	Root string

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
	c := &Client{Root: root, stateDir: stateDir}
	// 不设整体超时: ask --wait 长轮询与 SSE 都是长连接.
	c.http = &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			sock, err := c.socket()
			if err != nil {
				return nil, err
			}
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
	}}
	c.PairServiceClient = pairv1connect.NewPairServiceClient(connect.NewClient(connecthttp.NewTransport(c.http, baseURL)))
	return c
}

// socket 返回 daemon.json 记录的 socket 路径, 没有记录时按当前环境推算. 每次建立连接时重新读取,
// 使运行目录环境变量与常驻进程不同的 CLI 也能连上, 且不会被崩溃后残留的旧记录固定在失效的路径上.
func (c *Client) socket() (string, error) {
	if info, err := daemon.ReadInfo(c.stateDir); err == nil && info.Socket != "" {
		return info.Socket, nil
	}
	return daemon.SocketPath(c.Root)
}

func (c *Client) ping(ctx context.Context) (*pairv1.PingResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	return c.Ping(ctx, &pairv1.PingRequest{})
}

// Dial 连接 dir 所在工作树的常驻进程. 未运行时以 `pair daemon` 在后台拉起,
// 构建指纹与当前可执行文件不一致时请求旧进程退出、等它释放文件锁后再拉起.
// 多个 CLI 并发执行时各自循环到连上同一个指纹一致的实例为止.
func Dial(ctx context.Context, dir string) (*Client, error) {
	c, err := open(ctx, dir)
	if err != nil {
		return nil, err
	}
	var dialErr error
	ok := waitUntil(ctx, func() bool {
		res, err := c.ping(ctx)
		switch {
		case err == nil && res.Fingerprint == daemon.Fingerprint():
			return true
		case err == nil:
			// 旧版本实例; 它可能已在响应其他 CLI 的请求而退出, 传输错误不必处理.
			_, _ = c.Shutdown(ctx, &pairv1.ShutdownRequest{})
		case !c.locked():
			if dialErr = c.spawn(ctx); dialErr != nil {
				return true
			}
			// 连上的也可能是其他 CLI 拉起的实例, 仍要核对指纹; spawn 可能已用掉大部分等待时间, 这里立即核对.
			res, err := c.ping(ctx)
			return err == nil && res.Fingerprint == daemon.Fingerprint()
		}
		return false
	})
	switch {
	case dialErr != nil:
		return nil, dialErr
	case !ok && c.locked():
		return nil, c.unreachable()
	case !ok:
		return nil, fmt.Errorf("常驻进程未能在 %s 内就绪", waitTimeout)
	}
	return c, nil
}

// unreachable 返回常驻进程持有文件锁却无法连接时的错误.
func (c *Client) unreachable() error {
	sock, err := c.socket()
	if err != nil {
		return err
	}
	pid := "<pid>"
	if info, err := daemon.ReadInfo(c.stateDir); err == nil {
		pid = strconv.Itoa(info.PID)
	}
	return fmt.Errorf("常驻进程持有 %s, 但无法经 %s 连接 (socket 文件可能已被清理).\n修复操作: 执行 `kill %s` 结束该进程后重试",
		daemon.LockPath(c.stateDir), sock, pid)
}

// spawn 以脱离终端的子进程拉起 `pair daemon` 并等待它可用. 多个 CLI 同时拉起时,
// 后到的实例因文件锁退出, 轮询会连上先到的实例. 工作树未初始化时不拉起, 也不写运行态文件.
func (c *Client) spawn(ctx context.Context) error {
	if _, err := os.Stat(filepath.Join(c.Root, repo.Dir)); errors.Is(err, fs.ErrNotExist) {
		return repo.ErrNotInitialized
	}
	// 常驻进程继承当前环境推算 socket 路径, 推算失败时直接报错而不拉起注定失败的进程.
	if _, err := daemon.SocketPath(c.Root); err != nil {
		return err
	}
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
		// 退出原因写在日志里, 启动失败时随日志末尾一起报告.
		_ = cmd.Wait()
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

// locked 报告是否有常驻进程持有工作树的文件锁. 运行态目录尚不存在等无法加锁的情况视为没有.
func (c *Client) locked() bool {
	lock := flock.New(daemon.LockPath(c.stateDir))
	ok, err := lock.TryLock()
	if ok {
		_ = lock.Unlock()
	}
	return !ok && err == nil
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

// Stop 停止 dir 所在工作树的常驻进程, 报告此前是否在运行. 持有文件锁却无法连接时返回带修复命令的错误.
func Stop(ctx context.Context, dir string) (bool, error) {
	c, err := open(ctx, dir)
	if err != nil {
		return false, err
	}
	if _, err := c.ping(ctx); err != nil {
		if c.locked() {
			return false, c.unreachable()
		}
		return false, nil
	}
	// socket 在关闭 HTTP 服务时就已不可连接, 但锁要到业务实现关闭后才释放, 此前拉起的新实例会因锁被占用而退出.
	_, _ = c.Shutdown(ctx, &pairv1.ShutdownRequest{})
	if !waitUntil(ctx, func() bool { return !c.locked() }) {
		return true, fmt.Errorf("常驻进程未能在 %s 内退出", waitTimeout)
	}
	return true, nil
}
