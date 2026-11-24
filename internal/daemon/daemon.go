// Package daemon 运行工作树常驻进程.
//
// 同一套 HTTP 处理逻辑同时监听工作树私有的 Unix Domain Socket (供 CLI, 不校验令牌)
// 与 127.0.0.1 随机端口 (供界面, 校验访问令牌、Host 与 Origin). RPC 走 Connect,
// 事件流是独立的 SSE 端点 GET /events, 界面静态资源由 web 包内嵌.
package daemon

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/gofrs/flock"

	pairv1 "github.com/FooFishes/pairwise/gen/pair/v1"
	"github.com/FooFishes/pairwise/gen/pair/v1/pairv1connect"
	"github.com/FooFishes/pairwise/internal/gitx"
	"github.com/FooFishes/pairwise/internal/repo"
)

// Service 是常驻进程托管的业务实现, 由 core.Workspace 提供.
type Service interface {
	pairv1connect.PairServiceHandler
	// Subscribe 订阅此后追加的事件, cancel 后通道关闭.
	Subscribe() (events <-chan *pairv1.Event, cancel func())
	// EventsSince 返回序号大于 seq 的已持久化事件.
	EventsSince(seq int64) ([]*pairv1.Event, error)
	// Reload 在 .pair/ 文件变化后重建索引, 从不写回文件.
	Reload(ctx context.Context)
	// BranchChanged 在 HEAD 变化后按新分支重建索引并使旧分支的绑定失效.
	BranchChanged(ctx context.Context)
	// SetUIURL 告知界面地址(不含令牌), 供输出提示使用.
	SetUIURL(url string)
	// Done 在收到 Shutdown 请求后关闭.
	Done() <-chan struct{}
	Close() error
}

// Info 是 $GIT_DIR/pair/daemon.json 中记录的运行信息.
type Info struct {
	PID         int    `json:"pid"`
	Socket      string `json:"socket"`
	Port        int    `json:"port"`
	Fingerprint string `json:"fingerprint"`
	Version     string `json:"version"`
	StartedAt   int64  `json:"started_at"`
}

// Version 由构建时 -ldflags 注入.
var Version = "dev"

// SocketPath 返回工作树的 Unix Socket 路径: 运行目录 ($PAIR_RUNTIME_DIR, 其次 $XDG_RUNTIME_DIR,
// 再次系统临时目录) 下每用户私有目录中的 <工作树根路径 SHA-256 前 16 位>.sock.
// 私有目录不存在时以 0700 创建, 已存在时必须通过 checkPrivate.
func SocketPath(root string) (string, error) {
	runtime := cmp.Or(os.Getenv("PAIR_RUNTIME_DIR"), os.Getenv("XDG_RUNTIME_DIR"), os.TempDir())
	dir := filepath.Join(runtime, userDir)
	sum := sha256.Sum256([]byte(root))
	sock := filepath.Join(dir, hex.EncodeToString(sum[:8])+".sock")
	// sun_path 还要容纳结尾的空字符.
	if limit := len(syscall.RawSockaddrUnix{}.Path); len(sock) >= limit {
		return "", fmt.Errorf("socket 路径 %s 长 %d 字节, 加上结尾空字符超出系统上限 %d 字节.\n修复操作: 执行 `export PAIR_RUNTIME_DIR=/tmp` 改用更短的运行目录后重试",
			sock, len(sock), limit)
	}
	// 运行目录可以尚不存在; 私有目录位置上已有的任何东西 (文件、符号链接) 都交给 checkPrivate 判断.
	err := os.MkdirAll(runtime, 0o700)
	if err == nil {
		err = os.Mkdir(dir, 0o700)
	}
	if err != nil && !errors.Is(err, fs.ErrExist) {
		return "", fmt.Errorf("无法创建 socket 目录 %s: %w.\n修复操作: 执行 `export PAIR_RUNTIME_DIR=$HOME/.cache/pair` 改用当前用户可写的运行目录后重试", dir, err)
	}
	if err := checkPrivate(dir); err != nil {
		return "", err
	}
	return sock, nil
}

// InfoPath 返回 daemon.json 的路径.
func InfoPath(stateDir string) string {
	return filepath.Join(stateDir, "daemon.json")
}

// LockPath 返回保证每个工作树只有一个常驻进程的文件锁路径.
func LockPath(stateDir string) string {
	return filepath.Join(stateDir, "daemon.lock")
}

// ReadInfo 读取运行信息.
func ReadInfo(stateDir string) (*Info, error) {
	data, err := os.ReadFile(InfoPath(stateDir))
	if err != nil {
		return nil, err
	}
	info := new(Info)
	if err := json.Unmarshal(data, info); err != nil {
		return nil, err
	}
	return info, nil
}

// Fingerprint 返回当前可执行文件的构建指纹(大小与修改时间), 用于发现二进制已更新.
// 进程内首次调用时计算并缓存, 因此常驻进程报告的始终是它启动时的二进制.
func Fingerprint() string {
	return fingerprint()
}

var fingerprint = sync.OnceValue(func() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	fi, err := os.Stat(exe)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d-%d", fi.Size(), fi.ModTime().UnixNano())
})

// NewService 由 core 包注册, 避免 daemon 反向依赖 core 的具体实现.
type NewService func(ctx context.Context, dir string) (Service, error)

// Run 在前台运行 dir 所在工作树的常驻进程, 直到 ctx 结束或收到 Shutdown.
// 已有实例持有文件锁时立即返回错误.
func Run(ctx context.Context, dir string, newService NewService) error {
	fp := Fingerprint()
	r, err := gitx.Open(ctx, dir)
	if err != nil {
		return err
	}
	stateDir := filepath.Join(r.GitDir, "pair")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return err
	}
	lock := flock.New(LockPath(stateDir))
	locked, err := lock.TryLock()
	if err != nil {
		return err
	}
	if !locked {
		return errors.New("常驻进程已在运行")
	}
	// 释放锁放在最后: 锁一旦释放就可能有新实例启动, 此前必须已关闭业务实现.
	defer func() { _ = lock.Unlock() }()
	// 崩溃残留的运行信息指向已失效的 pid 与 socket, 拿到锁后立即清除.
	if err := os.Remove(InfoPath(stateDir)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	sock, err := SocketPath(r.Root)
	if err != nil {
		return err
	}
	// 崩溃残留的 socket 文件会使监听失败.
	if err := os.Remove(sock); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	svc, err := newService(ctx, r.Root)
	if err != nil {
		return err
	}
	defer svc.Close()

	// socket 位于私有目录中, 由目录权限隔离其他用户; UnixListener 关闭时自动删除 socket 文件.
	unixLn, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	defer unixLn.Close()
	tcpLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer tcpLn.Close()
	port := tcpLn.Addr().(*net.TCPAddr).Port

	info, err := json.Marshal(Info{
		PID:         os.Getpid(),
		Socket:      sock,
		Port:        port,
		Fingerprint: fp,
		Version:     Version,
		StartedAt:   time.Now().Unix(),
	})
	if err != nil {
		return err
	}
	if err := os.WriteFile(InfoPath(stateDir), info, 0o644); err != nil {
		return err
	}
	defer os.Remove(InfoPath(stateDir))
	svc.SetUIURL(fmt.Sprintf("http://127.0.0.1:%d/", port))

	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()
	pairDir, head := filepath.Join(r.Root, repo.Dir), filepath.Clean(r.HeadPath())
	if err := watchPaths(w, pairDir, head); err != nil {
		return err
	}

	// baseCtx 在关闭时取消, 结束 SSE、长轮询与文件监听; 文件监听退出后才关闭业务实现.
	var wg sync.WaitGroup
	defer wg.Wait()
	baseCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	wg.Go(func() { watch(baseCtx, w, pairDir, head, svc) })

	srv := &http.Server{
		Handler:           newHandler(svc, port),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return baseCtx },
	}
	srv.RegisterOnShutdown(cancel)
	served := make(chan error, 2)
	go func() { served <- srv.Serve(unixLn) }()
	go func() { served <- srv.Serve(tcpLn) }()

	select {
	case <-ctx.Done():
	case <-svc.Done():
	case err = <-served:
	}
	shutdownCtx, cancelShutdown := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancelShutdown()
	if srv.Shutdown(shutdownCtx) != nil {
		srv.Close()
	}
	return err
}

// TokenPath 是只在 Unix Socket 上开放的端点, POST 后生成一次性界面访问令牌并返回带令牌的地址.
const TokenPath = "/token"

// EventsPath 是 SSE 事件流端点.
const EventsPath = "/events"
