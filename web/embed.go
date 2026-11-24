// Package web 内嵌工作台前端构建产物.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS 返回构建产物的根目录; 前端尚未构建时其中只有 .gitkeep.
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
