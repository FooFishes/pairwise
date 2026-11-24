package core

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"connectrpc.com/connect/v2"

	pairv1 "github.com/FooFishes/pairwise/gen/pair/v1"
	"github.com/FooFishes/pairwise/internal/check"
	"github.com/FooFishes/pairwise/internal/doc"
	"github.com/FooFishes/pairwise/internal/repo"
	"github.com/FooFishes/pairwise/internal/store"
)

// historyLimit 是沿 Git 历史回溯基准与确认原文时最多检查的版本数.
const historyLimit = 200

// parseTarget 解析 doc 或 doc#sec, 不接受容器子项路径.
func parseTarget(s string) (doc.Target, error) {
	t, err := doc.ParseTarget(s)
	if err == nil && t.Item != "" {
		err = fmt.Errorf("定位路径 %q 指向容器子项, 此处只接受 doc 或 doc#sec", s)
	}
	if err != nil {
		return t, connect.NewError(connect.CodeInvalidArgument, err.Error()+
			".\n修复操作: 执行 `pair list` 查看文档 ID, 再执行 `pair show <doc>` 查看其 ## 区块 ID, 以 `<doc>#<sec>` 形式重试")
	}
	return t, nil
}

// findDoc 在模块与流程中查找可解析的文档.
func findDoc(ix *repo.Index, id string) (*repo.Entry, error) {
	e := ix.Doc(id)
	if e == nil {
		return nil, connect.NewError(connect.CodeNotFound,
			fmt.Sprintf("模块或流程「%s」不存在.\n修复操作: 执行 `pair list` 查看已有的模块与流程 ID", id))
	}
	if e.Doc == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Sprintf("%s 无法解析: %v, 不能按区块处理.\n修复操作: 执行 `pair show %s` 查看当前内容与 base, 再执行 `pair update %[3]s --base <hash> --file <doc.md>` 以完整文档 (含 Frontmatter) 覆盖", e.Rel, e.Err, e.ID))
	}
	return e, nil
}

// findSection 解析 doc#sec 并查找区块.
func findSection(ix *repo.Index, target string) (*repo.Entry, *doc.Section, error) {
	t, err := parseTarget(target)
	if err != nil {
		return nil, nil, err
	}
	if t.Section == "" {
		return nil, nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Sprintf("定位路径 %q 缺少区块 ID, 此处需要 doc#sec 形式.\n修复操作: 执行 `pair show %s` 查看区块 ID, 以 `%[2]s#<sec>` 形式重试", target, t.Doc))
	}
	e, err := findDoc(ix, t.Doc)
	if err != nil {
		return nil, nil, err
	}
	s := e.Doc.Section(t.Section)
	if s == nil {
		return nil, nil, connect.NewError(connect.CodeNotFound,
			fmt.Sprintf("「%s」中没有区块「%s」.\n修复操作: 执行 `pair show %[1]s` 查看已有的区块 ID", t.Doc, t.Section))
	}
	return e, s, nil
}

// ListDocs 列出文档摘要, kind 为空时列出全部.
func (w *Workspace) ListDocs(ctx context.Context, req *pairv1.ListDocsRequest) (*pairv1.ListDocsResponse, error) {
	ix, _, err := w.sync(ctx)
	if err != nil {
		return nil, err
	}
	resp := &pairv1.ListDocsResponse{}
	for _, e := range ix.Entries {
		if req.Kind == pairv1.DocKind_DOC_KIND_UNSPECIFIED || pairv1.DocKind(e.Kind) == req.Kind {
			resp.Docs = append(resp.Docs, summary(e))
		}
	}
	return resp, nil
}

// GetDoc 返回整份文档的结构化视图.
func (w *Workspace) GetDoc(ctx context.Context, req *pairv1.GetDocRequest) (*pairv1.GetDocResponse, error) {
	ix, branch, err := w.sync(ctx)
	if err != nil {
		return nil, err
	}
	e := lookupEntry(ix, req.Id, doc.Kind(req.Kind))
	if e == nil {
		return nil, connect.NewError(connect.CodeNotFound,
			fmt.Sprintf("文档「%s」不存在.\n修复操作: 执行 `pair list` 查看已有的文档 ID", req.Id))
	}
	d, err := w.toDoc(ix, branch, e)
	if err != nil {
		return nil, err
	}
	return &pairv1.GetDocResponse{Doc: d}, nil
}

// lookupEntry 按类型查找文档; kind 为零值时先查模块与流程, 再查问题.
func lookupEntry(ix *repo.Index, id string, kind doc.Kind) *repo.Entry {
	switch kind {
	case 0:
		if e := ix.Doc(id); e != nil {
			return e
		}
		return ix.Decision(id)
	case doc.KindDecision:
		return ix.Decision(id)
	}
	if e := ix.Doc(id); e != nil && e.Kind == kind {
		return e
	}
	return nil
}

// Show 返回整文件或单个区块的原文, 并把整文件写入基准快照.
func (w *Workspace) Show(ctx context.Context, req *pairv1.ShowRequest) (*pairv1.ShowResponse, error) {
	ix, branch, err := w.sync(ctx)
	if err != nil {
		return nil, err
	}
	t, err := parseTarget(req.Target)
	if err != nil {
		return nil, err
	}
	var e *repo.Entry
	if t.Section == "" {
		e = lookupEntry(ix, t.Doc, 0)
	} else if e, _, err = findSection(ix, req.Target); err != nil {
		return nil, err
	}
	if e == nil {
		return nil, connect.NewError(connect.CodeNotFound,
			fmt.Sprintf("文档或问题「%s」不存在.\n修复操作: 执行 `pair list` 查看已有的文档与问题 ID", t.Doc))
	}
	d, err := w.toDoc(ix, branch, e)
	if err != nil {
		return nil, err
	}
	if d.Base, err = w.store.PutBase([]byte(d.Raw)); err != nil {
		return nil, err
	}
	content := d.Raw
	if t.Section != "" {
		content = string(e.Doc.Text(e.Doc.Section(t.Section).Span))
	}
	return &pairv1.ShowResponse{Doc: d, SectionId: t.Section, Content: content, Base: d.Base}, nil
}

// Lookup 按源码路径反查 scope 命中的模块.
func (w *Workspace) Lookup(ctx context.Context, req *pairv1.LookupRequest) (*pairv1.LookupResponse, error) {
	ix, _, err := w.sync(ctx)
	if err != nil {
		return nil, err
	}
	resp := &pairv1.LookupResponse{}
	for _, p := range req.Paths {
		rel := w.relPath(p)
		r := &pairv1.LookupResult{Path: rel, Ignored: w.config().Ignored(rel)}
		if rel == ".." || strings.HasPrefix(rel, "../") {
			// 工作树外的路径不属于任何模块; CLI 据 ../ 前缀说明.
			resp.Results = append(resp.Results, r)
			continue
		}
		for _, e := range ix.ModulesFor(rel) {
			r.Docs = append(r.Docs, summary(e))
		}
		resp.Results = append(resp.Results, r)
	}
	return resp, nil
}

// relPath 把绝对路径或相对工作树根目录的路径转换为相对路径. 工作树根已解析过符号链接,
// 因此绝对路径也先解析符号链接; 文件可能已被删除或将要新建在尚不存在的目录中, 只解析最深的已存在祖先目录.
func (w *Workspace) relPath(p string) string {
	if !filepath.IsAbs(p) {
		return filepath.ToSlash(filepath.Clean(p))
	}
	for dir, rest := filepath.Dir(p), filepath.Base(p); ; dir, rest = filepath.Dir(dir), filepath.Join(filepath.Base(dir), rest) {
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			return w.repo.Rel(filepath.Join(real, rest))
		}
		if filepath.Dir(dir) == dir {
			return w.repo.Rel(p)
		}
	}
}

// Check 校验全部文档.
func (w *Workspace) Check(ctx context.Context, req *pairv1.CheckRequest) (*pairv1.CheckResponse, error) {
	ix, _, err := w.sync(ctx)
	if err != nil {
		return nil, err
	}
	resp := &pairv1.CheckResponse{}
	for _, d := range check.Run(ix, check.Options{Strict: req.Strict}) {
		resp.Diagnostics = append(resp.Diagnostics, toDiagnostic(d))
	}
	return resp, nil
}

// SectionDiff 返回区块确认原文与当前原文, 供界面高亮未审阅的改动.
func (w *Workspace) SectionDiff(ctx context.Context, req *pairv1.SectionDiffRequest) (*pairv1.SectionDiffResponse, error) {
	ix, _, err := w.sync(ctx)
	if err != nil {
		return nil, err
	}
	e, s, err := findSection(ix, req.Target)
	if err != nil {
		return nil, err
	}
	resp := &pairv1.SectionDiffResponse{Current: string(doc.Normalize(e.Doc.Text(s.Span)))}
	hash := metaOf(e.Doc, s.ID).ConfirmedHash
	if hash == "" {
		resp.Reason = "区块尚未确认"
		return resp, nil
	}
	confirmed, err := w.confirmedText(ctx, e, s.ID, hash)
	if isNotFound(err) {
		resp.Reason = "确认原文未入库"
		return resp, nil
	}
	if err != nil {
		return nil, err
	}
	resp.Available, resp.Confirmed = true, string(confirmed)
	return resp, nil
}

// confirmedText 读取区块确认原文; 本地未缓存时沿该文件的 Git 历史查找哈希匹配的版本并缓存.
func (w *Workspace) confirmedText(ctx context.Context, e *repo.Entry, secID, hash string) ([]byte, error) {
	text, err := w.store.GetConfirmed(hash)
	if !isNotFound(err) {
		return text, err
	}
	err = w.walkHistory(ctx, e.Rel, func(raw []byte) bool {
		d, perr := doc.Parse(raw, e.Kind)
		if perr != nil {
			return false
		}
		if s := d.Section(secID); s != nil && s.Hash == hash {
			text = doc.Normalize(d.Text(s.Span))
			return true
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	return text, w.store.PutConfirmed(hash, text)
}

// walkHistory 从新到旧逐版本读取 rel 的内容, match 返回真时停止; 没有匹配的版本时返回 store.ErrNotFound.
func (w *Workspace) walkHistory(ctx context.Context, rel string, match func(raw []byte) bool) error {
	revs, err := w.repo.Git.FileHistory(ctx, rel, historyLimit)
	if err != nil {
		return err
	}
	for _, rev := range revs {
		// 该版本中文件可能已被删除, 跳过读取失败的版本.
		if raw, err := w.repo.Git.ShowFile(ctx, rev, rel); err == nil && match(raw) {
			return nil
		}
	}
	return store.ErrNotFound
}

// Confirm 把区块 (省略 #sec 时为全部区块) 的当前哈希写入 confirmed_hash, 并清空其假设与撤回列表.
// 只改 Frontmatter, 正文字节不变.
func (w *Workspace) Confirm(ctx context.Context, req *pairv1.ConfirmRequest) (*pairv1.ConfirmResponse, error) {
	t, err := parseTarget(req.Target)
	if err != nil {
		return nil, err
	}
	w.wmu.Lock()
	defer w.wmu.Unlock()
	ix, _, err := w.sync(ctx)
	if err != nil {
		return nil, err
	}
	e, err := findDoc(ix, t.Doc)
	if err != nil {
		return nil, err
	}
	if t.Section != "" {
		if _, _, err := findSection(ix, req.Target); err != nil {
			return nil, err
		}
	}
	// 索引中的文档被并发读取, 修改 Frontmatter 前先解析出私有副本.
	d, err := doc.Parse(e.Doc.Raw, e.Kind)
	if err != nil {
		return nil, err
	}
	secs := d.Sections
	if t.Section != "" {
		secs = []*doc.Section{d.Section(t.Section)}
	}
	resp := &pairv1.ConfirmResponse{DocId: e.ID}
	dirty := false
	for _, s := range secs {
		if s.ID == "" {
			continue
		}
		if err := w.store.PutConfirmed(s.Hash, doc.Normalize(d.Text(s.Span))); err != nil {
			return nil, err
		}
		dirty = setMeta(d.Front, s.ID, doc.SectionMeta{ConfirmedHash: s.Hash}) || dirty
		resp.Sections = append(resp.Sections, s.ID)
	}
	raw, err := w.writeFront(e, d, dirty)
	if err != nil {
		return nil, err
	}
	if resp.Base, err = w.store.PutBase(raw); err != nil {
		return nil, err
	}
	return resp, nil
}

// AckRetracted 移除区块中的一条撤回假设.
func (w *Workspace) AckRetracted(ctx context.Context, req *pairv1.AckRetractedRequest) (*pairv1.AckRetractedResponse, error) {
	w.wmu.Lock()
	defer w.wmu.Unlock()
	ix, _, err := w.sync(ctx)
	if err != nil {
		return nil, err
	}
	e, s, err := findSection(ix, req.Target)
	if err != nil {
		return nil, err
	}
	// 索引中的文档被并发读取, 修改 Frontmatter 前先解析出私有副本.
	d, err := doc.Parse(e.Doc.Raw, e.Kind)
	if err != nil {
		return nil, err
	}
	m := metaOf(d, s.ID)
	n := len(m.Retracted)
	m.Retracted = slices.DeleteFunc(slices.Clone(m.Retracted), func(a doc.Assumption) bool { return a.ID == req.AssumptionId })
	if len(m.Retracted) == n {
		return nil, connect.NewError(connect.CodeNotFound,
			fmt.Sprintf("区块「%s」中没有撤回假设「%s」.\n修复操作: 执行 `pair show %[1]s` 查看该区块的 retracted 列表", req.Target, req.AssumptionId))
	}
	setMeta(d.Front, s.ID, m)
	if _, err := w.writeFront(e, d, true); err != nil {
		return nil, err
	}
	return &pairv1.AckRetractedResponse{}, nil
}

// writeFront 在 Frontmatter 有变化时写回 d, 正文字节不变, 返回写回后的整文件.
func (w *Workspace) writeFront(e *repo.Entry, d *doc.Document, dirty bool) ([]byte, error) {
	if !dirty {
		return d.Raw, nil
	}
	raw := render(d)
	if err := repo.WriteFileAtomic(e.Path, raw); err != nil {
		return nil, err
	}
	_, err := w.reload()
	return raw, err
}
