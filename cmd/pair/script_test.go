package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

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
	})
}
