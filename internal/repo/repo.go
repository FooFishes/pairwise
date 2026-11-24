// Package repo 定位工作树中的 .pair/ 目录, 读取配置并加载全部文档.
package repo

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/pelletier/go-toml/v2"

	"github.com/FooFishes/pairwise/internal/doc"
	"github.com/FooFishes/pairwise/internal/gitx"
)

// Dir 是仓库内文档目录名.
const Dir = ".pair"

// ErrNotInitialized 表示工作树中还没有 .pair/ 目录.
var ErrNotInitialized = errors.New("当前工作树尚未初始化 Pairwise, 请先执行 `pair init`")

// Config 是 .pair/config.toml. comment 标签是 pair init 写入默认配置时的字段注释.
type Config struct {
	// IgnoreGlobs 是 check --diff 忽略的源码路径, 相对工作树根目录.
	IgnoreGlobs []string `toml:"ignore_globs" comment:"check --diff 忽略的源码路径, 相对工作树根目录, 使用 doublestar Glob 语法, 如 \"vendor/**\""`
	// AskWaitTimeoutSecs 是 ask --wait 的默认超时秒数.
	AskWaitTimeoutSecs int `toml:"ask_wait_timeout_secs" comment:"ask --wait 的默认超时秒数, 应低于宿主单次命令的超时上限"`
}

// DefaultConfig 返回内置默认配置.
func DefaultConfig() Config {
	return Config{AskWaitTimeoutSecs: 90}
}

// DefaultConfigTOML 是 pair init 写入的 config.toml 内容.
func DefaultConfigTOML() []byte {
	b, err := toml.Marshal(DefaultConfig())
	// 内置结构体的编码失败只可能是程序缺陷.
	if err != nil {
		panic(err)
	}
	return b
}

// LoadConfig 读取 config.toml, 文件不存在时返回默认配置, 缺省字段取默认值.
func LoadConfig(path string) (Config, error) {
	c := DefaultConfig()
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := toml.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s 格式不正确: %w", path, err)
	}
	if c.AskWaitTimeoutSecs <= 0 {
		return c, fmt.Errorf("%s: ask_wait_timeout_secs 必须是正整数", path)
	}
	for _, g := range c.IgnoreGlobs {
		if !doublestar.ValidatePattern(g) {
			return c, fmt.Errorf("%s: ignore_globs 中的 %q 不是合法的 Glob", path, g)
		}
	}
	return c, nil
}

// Repo 是已初始化 Pairwise 的工作树.
type Repo struct {
	Git *gitx.Repo
	// PairDir 是 <Root>/.pair.
	PairDir string
	// StateDir 是 $GIT_DIR/pair, 存放本地运行态.
	StateDir string
	Config   Config
}

// Root 返回工作树根目录.
func (r *Repo) Root() string {
	return r.Git.Root
}

// Open 从 dir 向上定位工作树并读取配置; .pair/ 不存在时返回 ErrNotInitialized 与已定位的 gitx.Repo.
// 未初始化时 PairDir 与 StateDir 同样已填好, 供 pair init 创建目录.
func Open(ctx context.Context, dir string) (*Repo, error) {
	g, err := gitx.Open(ctx, dir)
	if err != nil {
		return nil, err
	}
	r := &Repo{
		Git:      g,
		PairDir:  filepath.Join(g.Root, Dir),
		StateDir: filepath.Join(g.GitDir, "pair"),
		Config:   DefaultConfig(),
	}
	if _, err := os.Stat(r.PairDir); errors.Is(err, fs.ErrNotExist) {
		return r, ErrNotInitialized
	}
	if r.Config, err = LoadConfig(filepath.Join(r.PairDir, "config.toml")); err != nil {
		return nil, err
	}
	return r, nil
}

// Path 返回文档的绝对路径.
func (r *Repo) Path(kind doc.Kind, id string) string {
	return filepath.Join(r.PairDir, kind.Dir(), id+".md")
}

// Rel 把绝对路径转换为相对工作树根目录、以 / 分隔的路径.
func (r *Repo) Rel(abs string) string {
	rel, _ := filepath.Rel(r.Root(), abs)
	return filepath.ToSlash(rel)
}

// Ignored 报告相对路径是否命中 config.toml 的 ignore_globs. .pair/ 下的路径始终视为忽略.
func (r *Repo) Ignored(rel string) bool {
	return r.Config.Ignored(rel)
}

// Ignored 报告相对路径是否命中 ignore_globs. .pair/ 下的路径始终视为忽略.
func (c Config) Ignored(rel string) bool {
	if rel == Dir || strings.HasPrefix(rel, Dir+"/") {
		return true
	}
	return slices.ContainsFunc(c.IgnoreGlobs, func(g string) bool {
		return doublestar.MatchUnvalidated(g, rel)
	})
}

// Entry 是索引中的一份文档.
type Entry struct {
	Kind doc.Kind
	ID   string
	// Path 是绝对路径, Rel 是相对工作树根目录的路径.
	Path string
	Rel  string
	// Doc 在文件无法解析时为 nil, 此时 Err 记录原因.
	Doc *doc.Document
	Err error
}

// Index 是某一时刻 .pair/ 全部文档的快照.
type Index struct {
	// Entries 按类型(模块、流程、问题)再按 ID 排序.
	Entries []*Entry
}

// Load 读取 .pair/modules、.pair/flows 与 .pair/decisions 下的全部 .md 文件.
// 目录不存在视为没有该类文档, 子目录不递归.
func (r *Repo) Load() (*Index, error) {
	ix := &Index{}
	for _, kind := range []doc.Kind{doc.KindModule, doc.KindFlow, doc.KindDecision} {
		des, err := os.ReadDir(filepath.Join(r.PairDir, kind.Dir()))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, de := range des {
			id, ok := strings.CutSuffix(de.Name(), ".md")
			if !ok || de.IsDir() {
				continue
			}
			e := &Entry{Kind: kind, ID: id, Path: r.Path(kind, id)}
			e.Rel = r.Rel(e.Path)
			raw, err := os.ReadFile(e.Path)
			if err != nil {
				return nil, err
			}
			e.Doc, e.Err = doc.Parse(raw, kind)
			ix.Entries = append(ix.Entries, e)
		}
	}
	slices.SortFunc(ix.Entries, func(a, b *Entry) int {
		return cmp.Or(cmp.Compare(a.Kind, b.Kind), strings.Compare(a.ID, b.ID))
	})
	return ix, nil
}

// Doc 在模块与流程的共享命名空间中按 ID 查找.
func (ix *Index) Doc(id string) *Entry {
	return ix.find(func(e *Entry) bool { return e.Kind != doc.KindDecision && e.ID == id })
}

// Decision 按 ID 查找待确认问题.
func (ix *Index) Decision(id string) *Entry {
	return ix.find(func(e *Entry) bool { return e.Kind == doc.KindDecision && e.ID == id })
}

func (ix *Index) find(f func(*Entry) bool) *Entry {
	if i := slices.IndexFunc(ix.Entries, f); i >= 0 {
		return ix.Entries[i]
	}
	return nil
}

// Of 返回某一类型的全部文档.
func (ix *Index) Of(kind doc.Kind) []*Entry {
	var out []*Entry
	for _, e := range ix.Entries {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// ModulesFor 返回 scope 命中相对路径的模块, scope 使用 doublestar 语法.
// 不合法的 Glob 不命中任何路径, 由 pair check 报错.
func (ix *Index) ModulesFor(rel string) []*Entry {
	var out []*Entry
	for _, e := range ix.Of(doc.KindModule) {
		if e.Doc != nil && slices.ContainsFunc(e.Doc.Front.Scope, func(g string) bool {
			return doublestar.MatchUnvalidated(g, rel)
		}) {
			out = append(out, e)
		}
	}
	return out
}

// WriteFileAtomic 先写同目录临时文件再重命名, 避免读者看到写了一半的文件.
// 保留原文件的权限位, 新文件为 0644. 临时文件不以 .md 结尾, 不会被 Load 与文件监听当作文档.
func WriteFileAtomic(path string, data []byte) (err error) {
	perm := fs.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		perm = fi.Mode().Perm()
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
		}
	}()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Chmod(perm); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
