//go:build unix

package client

import (
	"os/exec"
	"syscall"
)

// detach 让子进程脱离当前终端会话, 终端关闭时不随之退出.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
