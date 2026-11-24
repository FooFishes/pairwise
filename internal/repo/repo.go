// Package repo 定位工作树中的 .pair/ 目录, 读取配置并加载全部文档.
package repo

import (
	_ "github.com/bmatcuk/doublestar/v4"
	_ "github.com/pelletier/go-toml/v2"

	"context"
	"errors"

	"github.com/FooFishes/pairwise/internal/doc"
	"github.com/FooFishes/pairwise/internal/gitx"
)

// Dir 是仓库内文档目录名.
const Dir = ".pair"

// ErrNotInitialized 表示工作树中还没有 .pair/ 目录.
var ErrNotInitialized = errors.New("当前工作树尚未初始化 Pairwise, 请先执行 `pair init`")

// Config 是 .pair/config.toml.
type Config struct {
	// IgnoreGlobs 是 check --diff 忽略的源码路径, 相对工作树根目录.
	IgnoreGlobs []string `toml:"ignore_globs"`
	// AskWaitTimeoutSecs 是 ask --wait 的默认超时秒数.
	AskWaitTimeoutSecs int `toml:"ask_wait_timeout_secs"`
}

// DefaultConfig 返回内置默认配置.
func DefaultConfig() Config {
	return Config{AskWaitTimeoutSecs: 90}
}

// DefaultConfigTOML 是 pair init 写入的 config.toml 内容.
func DefaultConfigTOML() []byte {
	panic("TODO")
}

// LoadConfig 读取 config.toml, 文件不存在时返回默认配置, 缺省字段取默认值.
func LoadConfig(path string) (Config, error) {
	panic("TODO")
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
func Open(ctx context.Context, dir string) (*Repo, error) {
	panic("TODO")
}

// Path 返回文档的绝对路径.
func (r *Repo) Path(kind doc.Kind, id string) string {
	panic("TODO")
}

// Rel 把绝对路径转换为相对工作树根目录、以 / 分隔的路径.
func (r *Repo) Rel(abs string) string {
	panic("TODO")
}

// Ignored 报告相对路径是否命中 config.toml 的 ignore_globs. .pair/ 下的路径始终视为忽略.
func (r *Repo) Ignored(rel string) bool {
	panic("TODO")
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
func (r *Repo) Load() (*Index, error) {
	panic("TODO")
}

// Doc 在模块与流程的共享命名空间中按 ID 查找.
func (ix *Index) Doc(id string) *Entry {
	panic("TODO")
}

// Decision 按 ID 查找待确认问题.
func (ix *Index) Decision(id string) *Entry {
	panic("TODO")
}

// Of 返回某一类型的全部文档.
func (ix *Index) Of(kind doc.Kind) []*Entry {
	panic("TODO")
}

// ModulesFor 返回 scope 命中相对路径的模块, scope 使用 doublestar 语法.
func (ix *Index) ModulesFor(rel string) []*Entry {
	panic("TODO")
}

// WriteFileAtomic 先写同目录临时文件再重命名, 避免读者看到写了一半的文件.
func WriteFileAtomic(path string, data []byte) error {
	panic("TODO")
}
