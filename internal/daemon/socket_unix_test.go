//go:build unix

package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSocketPath(t *testing.T) {
	// Socket 路径受系统长度上限约束, 不用很深的 t.TempDir().
	runtime, err := os.MkdirTemp("", "pair")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(runtime) })
	t.Setenv("PAIR_RUNTIME_DIR", runtime)

	sock, err := SocketPath("/repo")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(runtime, userDir)
	if filepath.Dir(sock) != dir || len(filepath.Base(sock)) != len("0123456789abcdef.sock") {
		t.Errorf("socket 路径 = %s", sock)
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("socket 目录: %v, %v", fi, err)
	}

	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := SocketPath("/repo"); err == nil || !strings.Contains(err.Error(), "`chmod 700 "+dir+"`") {
		t.Errorf("权限过宽: %v", err)
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	// 悬空的符号链接同样拒绝, 而不是跟随它创建目录.
	if err := os.Symlink(filepath.Join(runtime, "missing"), dir); err != nil {
		t.Fatal(err)
	}
	if _, err := SocketPath("/repo"); err == nil || !strings.Contains(err.Error(), "不是当前用户所有的目录") {
		t.Errorf("符号链接: %v", err)
	}

	t.Setenv("PAIR_RUNTIME_DIR", filepath.Join(runtime, strings.Repeat("d", 100)))
	if _, err := SocketPath("/repo"); err == nil || !strings.Contains(err.Error(), "`export PAIR_RUNTIME_DIR=/tmp`") {
		t.Errorf("路径超长: %v", err)
	}
}
