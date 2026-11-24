// Package cli 定义 pair 命令行. 默认文本输出面向模型阅读: 成功时附带下一步提示,
// 失败时给出原因与可直接执行的修复命令; --json 输出结构化结果.
package cli

import (
	_ "github.com/spf13/cobra"
)

// Execute 运行命令行并返回退出码.
func Execute() int {
	panic("TODO")
}
