// Package daemon 运行工作树常驻进程.
//
// 同一套 HTTP 处理逻辑同时监听工作树私有的 Unix Domain Socket (供 CLI, 不校验令牌)
// 与 127.0.0.1 随机端口 (供界面, 校验访问令牌、Host 与 Origin). RPC 走 Connect,
// 事件流是独立的 SSE 端点 GET /events, 界面静态资源由 web 包内嵌.
package daemon

import (
	_ "connectrpc.com/connect/v2/connecthttp"
	_ "github.com/fsnotify/fsnotify"
	_ "github.com/gofrs/flock"

	"context"

	pairv1 "github.com/FooFishes/pairwise/gen/pair/v1"
	"github.com/FooFishes/pairwise/gen/pair/v1/pairv1connect"
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

// SocketPath 返回工作树的 Unix Socket 路径: $PAIR_RUNTIME_DIR 或系统临时目录下的
// pair-<工作树根路径 SHA-256 前 16 位>.sock.
func SocketPath(root string) string {
	panic("TODO")
}

// InfoPath 返回 daemon.json 的路径.
func InfoPath(stateDir string) string {
	panic("TODO")
}

// ReadInfo 读取运行信息.
func ReadInfo(stateDir string) (*Info, error) {
	panic("TODO")
}

// Fingerprint 返回当前可执行文件的构建指纹(大小与修改时间), 用于发现二进制已更新.
func Fingerprint() string {
	panic("TODO")
}

// NewService 由 core 包注册, 避免 daemon 反向依赖 core 的具体实现.
type NewService func(ctx context.Context, dir string) (Service, error)

// Run 在前台运行 dir 所在工作树的常驻进程, 直到 ctx 结束或收到 Shutdown.
// 已有实例持有文件锁时立即返回错误.
func Run(ctx context.Context, dir string, newService NewService) error {
	panic("TODO")
}

// TokenPath 是只在 Unix Socket 上开放的端点, POST 后生成一次性界面访问令牌并返回带令牌的地址.
const TokenPath = "/token"

// EventsPath 是 SSE 事件流端点.
const EventsPath = "/events"
