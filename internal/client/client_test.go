package client

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"
	"sync"
	"testing"
)

// TestEventsReconnect 中每个连接只推送一条事件就断开, 客户端应凭最后序号带 Last-Event-ID 重连.
func TestEventsReconnect(t *testing.T) {
	// Socket 路径受系统长度上限约束, 不用很深的 t.TempDir().
	dir, err := os.MkdirTemp("", "pair")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("PAIR_RUNTIME_DIR", dir)
	c := newClient("/repo", dir)
	ln, err := net.Listen("unix", c.socket())
	if err != nil {
		t.Fatal(err)
	}
	var (
		mu      sync.Mutex
		lastIDs []string
	)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("Last-Event-ID")
		mu.Lock()
		lastIDs = append(lastIDs, id)
		mu.Unlock()
		seq, _ := strconv.Atoi(id)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, ": ping\n\nid: %d\ndata: {\"seq\":\"%d\"}\n\n", seq+1, seq+1)
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	events, errs := c.Events(ctx, 5)
	for want := int64(6); want <= 8; want++ {
		if ev := <-events; ev.GetSeq() != want {
			t.Fatalf("序号 = %d, 期望 %d", ev.GetSeq(), want)
		}
	}
	cancel()
	for range events {
	}
	for range errs {
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(lastIDs[:3], []string{"5", "6", "7"}) {
		t.Errorf("Last-Event-ID = %v", lastIDs)
	}
}
