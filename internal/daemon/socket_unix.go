//go:build unix

package daemon

import (
	"fmt"
	"os"
	"strconv"
	"syscall"
)

// userDir 是运行目录下当前用户私有的目录名.
var userDir = "pair-" + strconv.Itoa(os.Getuid())

// checkPrivate 确认 dir 是当前用户所有且组与其他用户没有任何权限的目录.
// 运行目录可能是多用户共用的临时目录, 否则其他用户可以抢先创建它来冒充常驻进程, 或绕过界面令牌直接连接.
func checkPrivate(dir string) error {
	fi, err := os.Lstat(dir)
	switch {
	case err != nil:
		return err
	case !fi.IsDir() || fi.Sys().(*syscall.Stat_t).Uid != uint32(os.Getuid()):
		return fmt.Errorf("socket 目录 %s 不是当前用户所有的目录 (可能被其他用户抢先创建).\n修复操作: 执行 `export PAIR_RUNTIME_DIR=$HOME/.cache/pair` 改用当前用户自己的运行目录后重试", dir)
	case fi.Mode().Perm()&0o077 != 0:
		return fmt.Errorf("socket 目录 %s 的权限为 %o, 其他用户可以访问其中的 socket.\n修复操作: 执行 `chmod 700 %s` 后重试", dir, fi.Mode().Perm(), dir)
	}
	return nil
}
