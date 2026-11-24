package core

import (
	"os"
	"slices"

	pairv1 "github.com/FooFishes/pairwise/gen/pair/v1"
	"github.com/FooFishes/pairwise/internal/check"
	"github.com/FooFishes/pairwise/internal/doc"
	"github.com/FooFishes/pairwise/internal/repo"
)

// doc 包与 proto 的 Kind、Status、BlockKind、Severity 枚举取值一一对应, 直接转换.

// summary 统计文档的区块状态、假设与过渡项数量.
func summary(e *repo.Entry) *pairv1.DocSummary {
	s := &pairv1.DocSummary{Id: e.ID, Kind: pairv1.DocKind(e.Kind), Path: e.Rel}
	d := e.Doc
	if d == nil {
		return s
	}
	s.Title = d.Title
	s.SectionCount = int32(len(d.Sections))
	s.WorkaroundCount = workarounds(d.PreambleBlocks)
	for _, sec := range d.Sections {
		switch d.Status(sec) {
		case doc.StatusUnconfirmed:
			s.UnconfirmedCount++
		case doc.StatusChanged:
			s.ChangedCount++
		}
		if m := d.Meta(sec.ID); m != nil {
			s.AssumptionCount += int32(len(m.Assumptions))
			s.RetractedCount += int32(len(m.Retracted))
		}
		s.WorkaroundCount += workarounds(sec.Blocks)
	}
	return s
}

func workarounds(bs []doc.Block) int32 {
	var n int32
	for _, b := range bs {
		if b.Kind == doc.BlockWorkaround {
			n++
		}
	}
	return n
}

// toDoc 转换整份文档. base 只计算不写快照; 无法解析的文档只带原文与诊断.
func (w *Workspace) toDoc(ix *repo.Index, branch string, e *repo.Entry) (*pairv1.Doc, error) {
	out := &pairv1.Doc{Id: e.ID, Kind: pairv1.DocKind(e.Kind), Path: e.Rel}
	for _, d := range check.Doc(ix, e) {
		out.Diagnostics = append(out.Diagnostics, toDiagnostic(check.Diagnostic{Diagnostic: d, Path: e.Rel}))
	}
	d := e.Doc
	if d == nil {
		raw, err := os.ReadFile(e.Path)
		out.Raw = string(raw)
		return out, err
	}
	out.Title = d.Title
	out.Base = doc.FileHash(d.Raw)
	out.Scope = d.Front.Scope
	out.PreambleMarkdown = string(d.Text(d.Preamble))
	out.PreambleBlocks = toBlocks(d, d.PreambleBlocks)
	out.Raw = string(d.Raw)
	for _, s := range d.Sections {
		out.Sections = append(out.Sections, toSection(d, s))
	}
	switch e.Kind {
	case doc.KindFlow:
		for _, r := range d.Refs {
			if r.Target.Doc != e.ID && !slices.Contains(out.Refs, r.Target.Doc) {
				out.Refs = append(out.Refs, r.Target.Doc)
			}
		}
	case doc.KindDecision:
		dec, err := w.toDecision(branch, e.ID, d)
		if err != nil {
			return nil, err
		}
		out.Decision = dec
	}
	return out, nil
}

func toSection(d *doc.Document, s *doc.Section) *pairv1.Section {
	out := &pairv1.Section{
		Id:       s.ID,
		Title:    s.Title,
		Hash:     s.Hash,
		Status:   pairv1.SectionStatus(d.Status(s)),
		Markdown: string(d.Text(s.Body)),
		Blocks:   toBlocks(d, s.Blocks),
		Line:     int32(s.Line),
	}
	if m := d.Meta(s.ID); m != nil {
		out.ConfirmedHash = m.ConfirmedHash
		out.Assumptions = toAssumptions(m.Assumptions)
		out.Retracted = toAssumptions(m.Retracted)
	}
	for _, r := range s.Refs {
		out.Refs = append(out.Refs, r.Target.String())
	}
	return out
}

func toAssumptions(as []doc.Assumption) []*pairv1.Assumption {
	var out []*pairv1.Assumption
	for _, a := range as {
		out = append(out, &pairv1.Assumption{Id: a.ID, Text: a.Text})
	}
	return out
}

func toBlocks(d *doc.Document, bs []doc.Block) []*pairv1.Block {
	var out []*pairv1.Block
	for _, b := range bs {
		out = append(out, &pairv1.Block{
			Kind:      pairv1.BlockKind(b.Kind),
			Markdown:  string(d.Text(b.Span)),
			Code:      b.Code,
			Layout:    toLayout(b.Layout),
			Id:        b.ID,
			Items:     toItems(d, b.Items),
			Condition: b.Condition,
		})
	}
	return out
}

func toItems(d *doc.Document, items []doc.Item) []*pairv1.BlockItem {
	var out []*pairv1.BlockItem
	for _, it := range items {
		out = append(out, &pairv1.BlockItem{
			Id:       it.ID,
			Title:    it.Title,
			Markdown: string(d.Text(it.Body)),
			Blocks:   toBlocks(d, it.Blocks),
		})
	}
	return out
}

func toLayout(ns []*doc.LayoutNode) []*pairv1.LayoutNode {
	var out []*pairv1.LayoutNode
	for _, n := range ns {
		out = append(out, &pairv1.LayoutNode{Label: n.Label, Children: toLayout(n.Children)})
	}
	return out
}

func toDiagnostic(d check.Diagnostic) *pairv1.Diagnostic {
	return &pairv1.Diagnostic{
		Severity: pairv1.Severity(d.Severity),
		Path:     d.Path,
		Line:     int32(d.Line),
		Code:     d.Code,
		Message:  d.Message,
		Fix:      d.Fix,
	}
}

// toDecision 转换问题: 首个 pw:compare 的方案与存储中的闭环进度.
func (w *Workspace) toDecision(branch, id string, d *doc.Document) (*pairv1.Decision, error) {
	out := &pairv1.Decision{Targets: d.Front.Targets, DefaultOption: d.Front.DefaultOption}
	if c := firstCompare(d); c != nil {
		out.Options = toItems(d, c.Items)
	}
	progress, err := w.store.Progress(branch, id)
	if err != nil && !isNotFound(err) {
		return nil, err
	}
	out.Progress = progress
	return out, nil
}

// compares 返回文档中全部 pw:compare 容器.
func compares(d *doc.Document) []doc.Block {
	blocks := slices.Clone(d.PreambleBlocks)
	for _, s := range d.Sections {
		blocks = append(blocks, s.Blocks...)
	}
	return slices.DeleteFunc(blocks, func(b doc.Block) bool { return b.Kind != doc.BlockCompare })
}

func firstCompare(d *doc.Document) *doc.Block {
	if cs := compares(d); len(cs) > 0 {
		return &cs[0]
	}
	return nil
}

// option 在全部 pw:compare 容器中按 ID 查找方案.
func option(d *doc.Document, id string) *doc.Item {
	for _, c := range compares(d) {
		for i := range c.Items {
			if c.Items[i].ID == id {
				return &c.Items[i]
			}
		}
	}
	return nil
}
