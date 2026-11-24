// Package core 实现常驻进程托管的业务操作, 直接实现生成的 PairServiceHandler.
package core

import (
	"context"

	pairv1 "github.com/FooFishes/pairwise/gen/pair/v1"
	"github.com/FooFishes/pairwise/gen/pair/v1/pairv1connect"
	"github.com/FooFishes/pairwise/internal/daemon"
)

// Workspace 是一个工作树的业务状态: 文档索引、运行态存储与事件订阅.
type Workspace struct {
	pairv1connect.UnimplementedPairServiceHandler
}

var _ daemon.Service = (*Workspace)(nil)

// New 打开 dir 所在工作树, 供 daemon.Run 使用.
func New(ctx context.Context, dir string) (daemon.Service, error) {
	panic("TODO")
}

// Subscribe 订阅此后追加的事件.
func (w *Workspace) Subscribe() (<-chan *pairv1.Event, func()) {
	panic("TODO")
}

// EventsSince 返回序号大于 seq 的已持久化事件.
func (w *Workspace) EventsSince(seq int64) ([]*pairv1.Event, error) {
	panic("TODO")
}

// Reload 重建文档索引.
func (w *Workspace) Reload(ctx context.Context) {
	panic("TODO")
}

// BranchChanged 按新分支重建索引并使旧分支的绑定失效.
func (w *Workspace) BranchChanged(ctx context.Context) {
	panic("TODO")
}

// SetUIURL 记录界面地址.
func (w *Workspace) SetUIURL(url string) {
	panic("TODO")
}

// Done 在收到 Shutdown 请求后关闭.
func (w *Workspace) Done() <-chan struct{} {
	panic("TODO")
}

// Close 释放存储.
func (w *Workspace) Close() error {
	panic("TODO")
}
