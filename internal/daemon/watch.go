package daemon

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// debounce 合并编辑器保存、git checkout 等操作产生的一串文件事件.
const debounce = 100 * time.Millisecond

// watchPaths 开始监听 .pair/ 目录树、工作树根目录与 GitDir.
// 根目录用来发现 .pair/ 被删除后重建 (如切到没有 .pair/ 的分支再切回), 此时原监听已随目录失效;
// GitDir 用来发现 HEAD 变化, git 以重命名替换 HEAD, 单独监听文件会在替换后失效.
func watchPaths(w *fsnotify.Watcher, pairDir, head string) error {
	for _, dir := range []string{filepath.Dir(pairDir), filepath.Dir(head)} {
		if err := w.Add(dir); err != nil {
			return err
		}
	}
	if err := addTree(w, pairDir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// watch 把 .pair/ 与 HEAD 的变化去抖后转给 svc, 直到 ctx 结束. 只读不写.
func watch(ctx context.Context, w *fsnotify.Watcher, pairDir, head string, svc Service) {
	root, gitDir := filepath.Dir(pairDir), filepath.Dir(head)
	var reload, branch <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-w.Events:
			switch dir := filepath.Dir(ev.Name); {
			case ev.Op == fsnotify.Chmod:
			case ev.Name == head:
				branch = time.After(debounce)
			case dir == gitDir, dir == root && ev.Name != pairDir:
			default:
				if ev.Has(fsnotify.Create) {
					// 新建目录在加入监听前可能已有内容, 整棵加入.
					if err := addTree(w, ev.Name); err != nil && !errors.Is(err, fs.ErrNotExist) {
						slog.Warn("加入文件监听失败", "path", ev.Name, "err", err)
					}
				}
				reload = time.After(debounce)
			}
		case err := <-w.Errors:
			slog.Warn("文件监听出错", "err", err)
		case <-reload:
			reload = nil
			svc.Reload(ctx)
		case <-branch:
			branch = nil
			svc.BranchChanged(ctx)
		}
	}
}

// addTree 监听 root 及其下全部子目录; root 是文件时什么也不做.
func addTree(w *fsnotify.Watcher, root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return err
		}
		return w.Add(path)
	})
}
