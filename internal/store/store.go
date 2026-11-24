// Package store 持久化当前工作树的本地运行态, 位于 $GIT_DIR/pair/, 不污染 git status.
//
// 结构化状态存入 pair.db (SQLite, WAL); 整文件基准快照存为 bases/<hash>,
// 区块确认原文存为 confirmed/<hash>. 时间统一为 Unix 毫秒.
package store

import (
	_ "modernc.org/sqlite"

	"errors"

	pairv1 "github.com/FooFishes/pairwise/gen/pair/v1"
)

// ErrNotFound 表示条目不存在.
var ErrNotFound = errors.New("not found")

// Store 是打开的运行态存储, 可被多个 goroutine 并发使用.
type Store struct{}

// Open 打开或创建 stateDir 下的运行态存储并执行表结构迁移.
func Open(stateDir string) (*Store, error) {
	panic("TODO")
}

// Close 关闭数据库.
func (s *Store) Close() error {
	panic("TODO")
}

// ---------- 快照 ----------

// PutBase 把整文件内容存入 bases/<FileHash>, 返回哈希; 已存在时只刷新访问时间.
// 写入后按最近 7 天或最近 100 份淘汰旧快照.
func (s *Store) PutBase(raw []byte) (string, error) {
	panic("TODO")
}

// GetBase 读取基准快照, 不存在时返回 ErrNotFound.
func (s *Store) GetBase(hash string) ([]byte, error) {
	panic("TODO")
}

// PutConfirmed 把区块归一化原文存入 confirmed/<hash>.
func (s *Store) PutConfirmed(hash string, normalized []byte) error {
	panic("TODO")
}

// GetConfirmed 读取区块确认原文, 不存在时返回 ErrNotFound.
func (s *Store) GetConfirmed(hash string) ([]byte, error) {
	panic("TODO")
}

// ---------- 反馈 ----------
//
// 反馈条目按分支隔离. 待确认问题条目的 ID 是 decision ID, 同一分支同一问题同时只有一条未闭环条目;
// 区块批注的 ID 是 fb-<自增序号>. 返回的 pairv1.Feedback 只填充存储中有的字段,
// pick_title、assumption_text 之外的展示信息由调用方补充.

// StageDecision 暂存对问题的选择. 已有暂存条目时覆盖所选方案与说明;
// 已发送但未闭环时返回错误.
func (s *Store) StageDecision(branch, decisionID, pick, note string, targets []string) (*pairv1.Feedback, error) {
	panic("TODO")
}

// StageNote 暂存针对区块或其中某条假设的修改说明.
func (s *Store) StageNote(branch, target, assumptionID, assumptionText, message string) (*pairv1.Feedback, error) {
	panic("TODO")
}

// RemoveStaged 删除一条尚未发送的暂存条目.
func (s *Store) RemoveStaged(branch, id string) error {
	panic("TODO")
}

// Feedback 查找当前分支上某 ID 最新的一条条目.
func (s *Store) Feedback(branch, id string) (*pairv1.Feedback, error) {
	panic("TODO")
}

// ListFeedback 按创建顺序列出分支上处于给定状态的条目, states 为空时列出全部未闭环条目.
func (s *Store) ListFeedback(branch string, states ...pairv1.FeedbackState) ([]*pairv1.Feedback, error) {
	panic("TODO")
}

// Trigger 把分支上全部暂存条目标记为已发送.
func (s *Store) Trigger(branch string, steer bool) ([]*pairv1.Feedback, error) {
	panic("TODO")
}

// Drain 把分支上全部已发送条目标记为已读取并返回它们.
func (s *Store) Drain(branch, sessionID string) ([]*pairv1.Feedback, error) {
	panic("TODO")
}

// MarkDrained 把单条已发送条目标记为已读取, 供 ask --wait 使用.
func (s *Store) MarkDrained(branch, id, sessionID string) error {
	panic("TODO")
}

// Resolve 把条目标记为已闭环.
func (s *Store) Resolve(branch, id string) error {
	panic("TODO")
}

// SetProgress 记录问题某个 target 的闭环进度, status 为 updated 或 no_change.
func (s *Store) SetProgress(branch, decisionID, target, status, reason string) error {
	panic("TODO")
}

// Progress 返回问题已记录的 target 进度.
func (s *Store) Progress(branch, decisionID string) ([]*pairv1.TargetProgress, error) {
	panic("TODO")
}

// ---------- 会话 ----------

// Binding 是会话对某个文档的绑定.
type Binding struct {
	ID        string
	SessionID string
	Branch    string
	DocID     string
	// Sections 为空表示绑定整个文档.
	Sections  []string
	Base      string
	Preserve  string
	Stale     bool
	CreatedAt int64
}

// Turn 是会话当前回合的累计状态, 供 before-stop 判断.
type Turn struct {
	SessionID string
	// Drained 是本回合读取过的反馈 ID.
	Drained []string
	// Updated 是本回合通过 update 写入过的文档 ID.
	Updated []string
	Asked   bool
	// Modified 是本回合改动过的源码相对路径.
	Modified []string
	Skipped  string
	// Blocked 是本回合已拦截过的原因哈希.
	Blocked []string
}

// UpsertSession 创建或更新会话; 新会话分配一个 4 位短码.
func (s *Store) UpsertSession(id, state string, capabilities []string) (*pairv1.Session, error) {
	panic("TODO")
}

// Session 按 ID 或短码查找会话.
func (s *Store) Session(idOrCode string) (*pairv1.Session, error) {
	panic("TODO")
}

// Sessions 列出最近 24 小时内活跃的会话.
func (s *Store) Sessions() ([]*pairv1.Session, error) {
	panic("TODO")
}

// AddBinding 记录绑定并分配 bind_id.
func (s *Store) AddBinding(b *Binding) (*Binding, error) {
	panic("TODO")
}

// Binding 按 bind_id 查找绑定.
func (s *Store) Binding(id string) (*Binding, error) {
	panic("TODO")
}

// Bindings 返回会话的有效绑定.
func (s *Store) Bindings(sessionID string) ([]*Binding, error) {
	panic("TODO")
}

// ActiveBindings 返回分支上全部会话的有效绑定.
func (s *Store) ActiveBindings(branch string) ([]*Binding, error) {
	panic("TODO")
}

// AttachBinding 把未关联会话的绑定关联到会话, 供 after-tool 兜底.
func (s *Store) AttachBinding(bindID, sessionID string) error {
	panic("TODO")
}

// InvalidateBindings 把不属于 branch 的绑定标记为失效.
func (s *Store) InvalidateBindings(branch string) error {
	panic("TODO")
}

// Turn 返回会话当前回合的状态, 不存在时返回空回合.
func (s *Store) Turn(sessionID string) (*Turn, error) {
	panic("TODO")
}

// SaveTurn 保存回合状态.
func (s *Store) SaveTurn(t *Turn) error {
	panic("TODO")
}

// ResetTurn 在新回合开始时清空会话的回合状态.
func (s *Store) ResetTurn(sessionID string) error {
	panic("TODO")
}

// ---------- 跳过原因 ----------

// AddSkip 登记一条待写入提交信息的 Pair-Skip 原因, 相同原因不重复登记.
func (s *Store) AddSkip(reason, sessionID string) error {
	panic("TODO")
}

// PendingSkips 返回尚未消费的跳过原因.
func (s *Store) PendingSkips() ([]string, error) {
	panic("TODO")
}

// ConsumeSkips 清空待用的跳过原因.
func (s *Store) ConsumeSkips() error {
	panic("TODO")
}

// ---------- 事件 ----------

// AppendEvent 分配递增序号与时间后持久化事件, 并只保留最近 10000 条.
func (s *Store) AppendEvent(e *pairv1.Event) (*pairv1.Event, error) {
	panic("TODO")
}

// EventsSince 返回序号大于 seq 的事件, 按序号升序, 最多 limit 条.
func (s *Store) EventsSince(seq int64, limit int) ([]*pairv1.Event, error) {
	panic("TODO")
}
