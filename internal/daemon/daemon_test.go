package daemon

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// runStub 在真实 Git 仓库中以 stubService 运行 Run, 返回工作树根目录与 stub.
func runStub(t *testing.T, ctx context.Context) (string, *stubService, <-chan error) {
	runtime, err := os.MkdirTemp("", "pair")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(runtime) })
	t.Setenv("PAIR_RUNTIME_DIR", runtime)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root := t.TempDir()
	git(t, root, "init", "-q", "-b", "main")
	if err := os.MkdirAll(filepath.Join(root, ".pair", "modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	svc := &stubService{reloaded: make(chan struct{}, 8), branched: make(chan struct{}, 8), done: make(chan struct{})}
	ran := make(chan error, 1)
	go func() {
		ran <- Run(ctx, root, func(context.Context, string) (Service, error) { return svc, nil })
	}()
	stateDir := filepath.Join(root, ".git", "pair")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := ReadInfo(stateDir); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("常驻进程未写出 daemon.json")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return root, svc, ran
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func expect(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatalf("未收到%s通知", what)
	}
}

func TestRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root, svc, ran := runStub(t, ctx)
	stateDir := filepath.Join(root, ".git", "pair")
	info, err := ReadInfo(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(info.Socket); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket %s: %v, %v", info.Socket, fi, err)
	}

	if err := Run(ctx, root, nil); err == nil || err.Error() != "常驻进程已在运行" {
		t.Fatalf("第二个实例: %v", err)
	}

	pairDir := filepath.Join(root, ".pair")
	write := func(rel string) func() error {
		return func() error { return os.WriteFile(filepath.Join(pairDir, rel), []byte("# x\n"), 0o644) }
	}
	steps := []struct {
		what   string
		ch     chan struct{}
		change func() error
	}{
		{"文件变化", svc.reloaded, write("modules/a.md")},
		{"新建目录", svc.reloaded, func() error { return os.MkdirAll(filepath.Join(pairDir, "flows", "nested"), 0o755) }},
		{"新目录中的文件变化", svc.reloaded, write("flows/nested/b.md")},
		{"删除 .pair/", svc.reloaded, func() error { return os.RemoveAll(pairDir) }},
		{"重建 .pair/", svc.reloaded, func() error { return os.Mkdir(pairDir, 0o755) }},
		{"重建后的文件变化", svc.reloaded, write("c.md")},
		{"分支切换", svc.branched, func() error {
			return exec.Command("git", "-C", root, "checkout", "-q", "-b", "feature").Run()
		}},
	}
	for _, st := range steps {
		// 排空上一步的后续通知, 收到的通知只能来自本步改动.
		time.Sleep(3 * debounce)
		for len(st.ch) > 0 {
			<-st.ch
		}
		if err := st.change(); err != nil {
			t.Fatal(err)
		}
		expect(t, st.ch, st.what)
	}

	// 有 SSE 连接未断开时也能及时退出.
	res, err := unixClient(info.Socket).Get("http://pair" + EventsPath)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	close(svc.done)
	select {
	case err := <-ran:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("有 SSE 连接时未能及时退出")
	}
	for _, p := range []string{info.Socket, InfoPath(stateDir)} {
		if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("退出后 %s 仍存在: %v", p, err)
		}
	}
	if !svc.closed {
		t.Error("退出时未关闭业务实现")
	}
}
