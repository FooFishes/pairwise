package client

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// detach 让子进程脱离当前控制台, 控制台关闭或收到 Ctrl+C 时不随之退出.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP}
}
