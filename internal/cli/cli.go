// Package cli 定义 pair 命令行. 默认文本输出面向模型阅读: 成功时附带下一步提示,
// 失败时给出原因与可直接执行的修复命令; --json 输出结构化结果.
package cli

import (
	_ "github.com/spf13/cobra"

	"github.com/FooFishes/pairwise/internal/daemon"
)

// Execute 运行命令行并返回退出码. newService 用于隐藏子命令 pair daemon 启动常驻进程,
// 由 main 注入以免 cli 依赖 core.
func Execute(newService daemon.NewService) int {
	panic("TODO")
}
