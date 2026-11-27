package client

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// detach 让子进程使用自己的隐藏控制台, 当前控制台关闭或收到 Ctrl+C 时不随之退出.
// 不用 DETACHED_PROCESS: 没有控制台的进程每执行一次 git 都要新建控制台, 既慢又会弹出窗口,
// 多个工作树并发时 git 还会挂在新建控制台上.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP}
}
