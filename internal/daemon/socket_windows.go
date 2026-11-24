package daemon

// userDir 是运行目录下的 socket 目录名. Windows 没有 uid, 默认运行目录位于用户目录下, 由 ACL 隔离.
const userDir = "pair"

// checkPrivate 在 Windows 上不检查属主与权限位, 访问控制交给 ACL.
func checkPrivate(string) error { return nil }
