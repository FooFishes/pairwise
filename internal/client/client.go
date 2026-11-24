// Package client 是 CLI 侧对常驻进程的访问: 发现或拉起当前工作树的常驻进程,
// 经 Unix Socket 调用 Connect 接口并订阅 SSE 事件流.
package client

import (
	"context"

	pairv1 "github.com/FooFishes/pairwise/gen/pair/v1"
	"github.com/FooFishes/pairwise/gen/pair/v1/pairv1connect"
)

// Client 是到常驻进程的连接.
type Client struct {
	pairv1connect.PairServiceClient
	// Root 是工作树根目录.
	Root   string
	Socket string
}

// Dial 连接 dir 所在工作树的常驻进程. 未运行时以 `pair daemon` 在后台拉起,
// 构建指纹与当前可执行文件不一致时先停止旧进程再拉起.
func Dial(ctx context.Context, dir string) (*Client, error) {
	panic("TODO")
}

// Running 报告 dir 所在工作树的常驻进程是否在运行, 不会拉起.
func Running(ctx context.Context, dir string) (*Client, bool) {
	panic("TODO")
}

// Events 订阅 SSE 事件流, 从序号 since 之后开始, 断线时凭最后的序号自动重连.
// ctx 结束后两个通道都关闭.
func (c *Client) Events(ctx context.Context, since int64) (<-chan *pairv1.Event, <-chan error) {
	panic("TODO")
}

// UIURL 申请一次性访问令牌并返回带令牌的界面地址.
func (c *Client) UIURL(ctx context.Context) (string, error) {
	panic("TODO")
}

// Stop 停止 dir 所在工作树的常驻进程, 未运行时直接返回.
func Stop(ctx context.Context, dir string) error {
	panic("TODO")
}
