// Package store 持久化当前工作树的本地运行态, 位于 $GIT_DIR/pair/, 不污染 git status.
//
// 结构化状态存入 pair.db (SQLite, WAL); 整文件基准快照存为 bases/<hash>,
// 区块确认原文存为 confirmed/<hash>. 时间统一为 Unix 毫秒.
package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	_ "modernc.org/sqlite"

	pairv1 "github.com/FooFishes/pairwise/gen/pair/v1"
)

// ErrNotFound 表示条目不存在.
var ErrNotFound = errors.New("not found")

// ErrSent 表示问题已发送且尚未闭环, 不能再修改暂存的选择.
var ErrSent = errors.New("问题已发送, 等待闭环")

const (
	baseKeepAge   = 7 * 24 * time.Hour
	baseKeepCount = 100
	eventKeep     = 10000
	sessionActive = 24 * time.Hour
)

const (
	staged    = pairv1.FeedbackState_FEEDBACK_STATE_STAGED
	triggered = pairv1.FeedbackState_FEEDBACK_STATE_TRIGGERED
	drained   = pairv1.FeedbackState_FEEDBACK_STATE_DRAINED
	resolved  = pairv1.FeedbackState_FEEDBACK_STATE_RESOLVED
)

// Store 是打开的运行态存储, 可被多个 goroutine 并发使用.
type Store struct {
	db  *sql.DB
	dir string
	// mu 串行化 bases/ 的写入、访问时间刷新与淘汰.
	mu sync.Mutex
}

// Open 打开或创建 stateDir 下的运行态存储并执行表结构迁移.
func Open(stateDir string) (*Store, error) {
	for _, d := range []string{"bases", "confirmed"} {
		if err := os.MkdirAll(filepath.Join(stateDir, d), 0o755); err != nil {
			return nil, err
		}
	}
	// 事务一律以 BEGIN IMMEDIATE 开始, 避免读后写的事务升级写锁时直接返回 SQLITE_BUSY.
	// WAL 下 synchronous=NORMAL 只在 checkpoint 时刷盘: 提交不再逐次刷盘 (Windows 上刷盘很慢,
	// 并发写入者持锁过久会等满 busy_timeout); 代价是断电或系统崩溃时可能丢失最近的提交, 运行态可以接受.
	dsn := filepath.Join(stateDir, "pair.db") + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db, dir: stateDir}
	if err := s.tx(migrate); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close 关闭数据库.
func (s *Store) Close() error {
	return s.db.Close()
}

// ---------- 快照 ----------

// PutBase 把整文件内容存入 bases/<FileHash>, 返回哈希; 已存在时只刷新访问时间.
// 写入后按最近 7 天或最近 100 份淘汰旧快照.
func (s *Store) PutBase(raw []byte) (string, error) {
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:6])
	path := filepath.Join(s.dir, "bases", hash)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	err := os.Chtimes(path, now, now)
	if errors.Is(err, fs.ErrNotExist) {
		err = s.writeFile(path, raw)
	}
	if err != nil {
		return "", err
	}
	if err := s.evictBases(); err != nil {
		return "", err
	}
	return hash, nil
}

// GetBase 读取基准快照并刷新其访问时间, 不存在时返回 ErrNotFound.
func (s *Store) GetBase(hash string) ([]byte, error) {
	path, err := s.snapshot("bases", hash)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if err := os.Chtimes(path, now, now); err != nil {
		return nil, notFound(err)
	}
	raw, err := os.ReadFile(path)
	return raw, notFound(err)
}

// PutConfirmed 把区块归一化原文存入 confirmed/<hash>.
func (s *Store) PutConfirmed(hash string, normalized []byte) error {
	return s.writeFile(filepath.Join(s.dir, "confirmed", hash), normalized)
}

// GetConfirmed 读取区块确认原文, 不存在时返回 ErrNotFound.
func (s *Store) GetConfirmed(hash string) ([]byte, error) {
	path, err := s.snapshot("confirmed", hash)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	return raw, notFound(err)
}

// snapshot 返回快照文件路径; hash 来自外部输入, 不是 12 位十六进制时视为不存在, 防止路径穿越.
func (s *Store) snapshot(kind, hash string) (string, error) {
	if _, err := hex.DecodeString(hash); err != nil || len(hash) != 12 {
		return "", ErrNotFound
	}
	return filepath.Join(s.dir, kind, hash), nil
}

// writeFile 在 stateDir 写临时文件后重命名到 path, 保证快照要么完整要么不存在,
// 且淘汰扫描 bases/ 时看不到临时文件.
func (s *Store) writeFile(path string, data []byte) error {
	f, err := os.CreateTemp(s.dir, ".snapshot-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	// 快照目录只是缓存, 运行期间可能被手动清理.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// evictBases 删除既不在最近访问的 100 份之内、也超过 7 天未访问的基准快照.
func (s *Store) evictBases() error {
	dir := filepath.Join(s.dir, "bases")
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) <= baseKeepCount {
		return err
	}
	infos := make([]fs.FileInfo, len(entries))
	for i, e := range entries {
		if infos[i], err = e.Info(); err != nil {
			return err
		}
	}
	slices.SortFunc(infos, func(a, b fs.FileInfo) int { return b.ModTime().Compare(a.ModTime()) })
	cutoff := time.Now().Add(-baseKeepAge)
	for _, info := range infos[baseKeepCount:] {
		if info.ModTime().Before(cutoff) {
			if err := os.Remove(filepath.Join(dir, info.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func notFound(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNotFound
	}
	return err
}

// ---------- 反馈 ----------
//
// 反馈条目按分支隔离. 待确认问题条目的 ID 是 decision ID, 同一分支同一问题同时只有一条未闭环条目;
// 区块批注的 ID 是 fb-<自增序号>. 返回的 pairv1.Feedback 只填充存储中有的字段,
// pick_title、assumption_text 之外的展示信息由调用方补充.

const (
	feedbackCols = `id, kind, state, branch, pick, note, target, assumption_id, assumption_text,
	targets, progress, steer, drained_by, created_at, triggered_at, drained_at, resolved_at`
	selectFeedback = "SELECT " + feedbackCols + " FROM feedback "
)

func scanFeedback(sc scanner) (*pairv1.Feedback, error) {
	f := &pairv1.Feedback{}
	return f, sc.Scan(&f.Id, &f.Kind, &f.State, &f.Branch, &f.Pick, &f.Note, &f.Target, &f.AssumptionId, &f.AssumptionText,
		jsonCol{&f.Targets}, jsonCol{&f.Progress}, &f.Steer, &f.DrainedBy, &f.CreatedAt, &f.TriggeredAt, &f.DrainedAt, &f.ResolvedAt)
}

// StageDecision 暂存对问题的选择. 已有暂存条目时覆盖所选方案与说明;
// 已发送但未闭环时返回 ErrSent.
func (s *Store) StageDecision(branch, decisionID, pick, note string, targets []string) (*pairv1.Feedback, error) {
	// 冲突目标对应部分唯一索引 feedback_open; 未闭环条目不是暂存状态时 DO UPDATE 不生效, 也就没有返回行.
	f, err := one(s.db, scanFeedback, `INSERT INTO feedback (id, kind, state, branch, pick, note, targets, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (branch, id) WHERE state <> 4
		DO UPDATE SET pick = excluded.pick, note = excluded.note, targets = excluded.targets WHERE state = ?
		RETURNING `+feedbackCols,
		decisionID, pairv1.FeedbackKind_FEEDBACK_KIND_DECISION, staged, branch, pick, note, jsonCol{targets}, now(), staged)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrSent
	}
	return f, err
}

// StageNote 暂存针对区块或其中某条假设的修改说明.
func (s *Store) StageNote(branch, target, assumptionID, assumptionText, message string) (*pairv1.Feedback, error) {
	var f *pairv1.Feedback
	err := s.tx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO feedback (id, kind, state, branch, note, target, assumption_id, assumption_text, created_at)
			VALUES ('', ?, ?, ?, ?, ?, ?, ?, ?)`,
			pairv1.FeedbackKind_FEEDBACK_KIND_NOTE, staged, branch, message, target, assumptionID, assumptionText, now())
		if err != nil {
			return err
		}
		seq, _ := res.LastInsertId()
		f, err = one(tx, scanFeedback, `UPDATE feedback SET id = 'fb-' || seq WHERE seq = ? RETURNING `+feedbackCols, seq)
		return err
	})
	return f, err
}

// RemoveStaged 删除一条尚未发送的暂存条目, 没有该暂存条目时返回 ErrNotFound.
func (s *Store) RemoveStaged(branch, id string) error {
	return s.execOne(`DELETE FROM feedback WHERE branch = ? AND id = ? AND state = ?`, branch, id, staged)
}

// Feedback 查找当前分支上某 ID 最新的一条条目.
func (s *Store) Feedback(branch, id string) (*pairv1.Feedback, error) {
	return one(s.db, scanFeedback, selectFeedback+"WHERE branch = ? AND id = ? ORDER BY seq DESC LIMIT 1", branch, id)
}

// ListFeedback 按创建顺序列出分支上处于给定状态的条目, states 为空时列出全部未闭环条目.
func (s *Store) ListFeedback(branch string, states ...pairv1.FeedbackState) ([]*pairv1.Feedback, error) {
	if len(states) == 0 {
		states = []pairv1.FeedbackState{staged, triggered, drained}
	}
	return list(s.db, scanFeedback, selectFeedback+"WHERE branch = ? AND state IN (SELECT value FROM json_each(?)) ORDER BY seq",
		branch, jsonCol{states})
}

// Trigger 把分支上全部暂存条目标记为已发送.
func (s *Store) Trigger(branch string, steer bool) ([]*pairv1.Feedback, error) {
	return s.transit("state = ?, steer = ?, triggered_at = ?", "branch = ? AND state = ?",
		triggered, steer, now(), branch, staged)
}

// Drain 把分支上全部已发送条目标记为已读取并返回它们.
func (s *Store) Drain(branch, sessionID string) ([]*pairv1.Feedback, error) {
	return s.transit("state = ?, drained_by = ?, drained_at = ?", "branch = ? AND state = ?",
		drained, sessionID, now(), branch, triggered)
}

// MarkDrained 把单条已发送条目标记为已读取, 供 ask --wait 使用; 没有该已发送条目时返回 ErrNotFound.
func (s *Store) MarkDrained(branch, id, sessionID string) error {
	return s.execOne(`UPDATE feedback SET state = ?, drained_by = ?, drained_at = ? WHERE branch = ? AND id = ? AND state = ?`,
		drained, sessionID, now(), branch, id, triggered)
}

// Resolve 把已发送或已读取的条目标记为已闭环; 尚未发送的条目不能闭环, 返回 ErrNotFound.
func (s *Store) Resolve(branch, id string) error {
	return s.execOne(`UPDATE feedback SET state = ?, resolved_at = ? WHERE branch = ? AND id = ? AND state IN (?, ?)`,
		resolved, now(), branch, id, triggered, drained)
}

// SetProgress 记录问题某个 target 的闭环进度, status 为 updated 或 no_change.
// 进度记在已发送且未闭环的条目上, 没有这样的条目时返回 ErrNotFound.
func (s *Store) SetProgress(branch, decisionID, target, status, reason string) error {
	return s.tx(func(tx *sql.Tx) error {
		var seq int64
		var progress []*pairv1.TargetProgress
		err := tx.QueryRow(`SELECT seq, progress FROM feedback WHERE branch = ? AND id = ? AND state IN (?, ?)`,
			branch, decisionID, triggered, drained).Scan(&seq, jsonCol{&progress})
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		p := &pairv1.TargetProgress{Target: target, Status: status, Reason: reason}
		if i := slices.IndexFunc(progress, func(p *pairv1.TargetProgress) bool { return p.Target == target }); i >= 0 {
			progress[i] = p
		} else {
			progress = append(progress, p)
		}
		_, err = tx.Exec(`UPDATE feedback SET progress = ? WHERE seq = ?`, jsonCol{progress}, seq)
		return err
	})
}

// Progress 返回问题未闭环条目已记录的 target 进度; 已闭环的是上一轮问题的进度, 不计入.
func (s *Store) Progress(branch, decisionID string) ([]*pairv1.TargetProgress, error) {
	var progress []*pairv1.TargetProgress
	err := s.db.QueryRow(`SELECT progress FROM feedback WHERE branch = ? AND id = ? AND state <> ?`,
		branch, decisionID, resolved).Scan(jsonCol{&progress})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return progress, err
}

// transit 更新满足 cond 的条目, 按创建顺序返回更新后的条目. RETURNING 的行序不确定, 因此按序号回查.
func (s *Store) transit(set, cond string, args ...any) ([]*pairv1.Feedback, error) {
	var out []*pairv1.Feedback
	err := s.tx(func(tx *sql.Tx) error {
		seqs, err := list(tx, scanInt, "UPDATE feedback SET "+set+" WHERE "+cond+" RETURNING seq", args...)
		if err != nil {
			return err
		}
		out, err = list(tx, scanFeedback, selectFeedback+"WHERE seq IN (SELECT value FROM json_each(?)) ORDER BY seq", jsonCol{seqs})
		return err
	})
	return out, err
}

// ---------- 会话 ----------

// Session 是宿主会话.
type Session struct {
	ID string
	// Code 是 4 位短码, --session 可以用它代替会话 ID.
	Code         string
	State        string
	Capabilities []string
	// EnvFile 表示最近一次 session-start 时宿主提供了会话环境文件, 会话中的命令已带 PAIR_SESSION_ID.
	EnvFile bool
	// Unverified 是 before-stop 第二次放行时仍未解决的拦截原因, 新回合开始时清空.
	Unverified string
	UpdatedAt  int64
}

// Binding 是会话对某个文档的绑定. 一次 bind 涉及的各文档共用一个 ID.
type Binding struct {
	ID        string
	SessionID string
	Branch    string
	DocID     string
	// Sections 为空表示绑定整个文档.
	Sections []string
	Base     string
	Preserve string
}

// Turn 是会话当前回合的累计状态, 供 before-tool 与 before-stop 判断.
type Turn struct {
	SessionID string
	// Drained 是本回合读取过的反馈 ID.
	Drained []string
	// Updated 是本回合通过 update 写入过的文档 ID.
	Updated []string
	Asked   bool
	// Modified 是本回合改动过的源码相对路径.
	Modified []string
	// Skipped 是本回合 skip-update 登记的原因.
	Skipped string
	// Hinted 是本回合已提示过 pair bind 的模块 ID.
	Hinted []string
	// Blocked 是本回合已拦截过的原因哈希.
	Blocked []string
}

const selectSession = `SELECT id, code, state, capabilities, env_file, unverified, updated_at FROM sessions `

func scanSession(sc scanner) (*Session, error) {
	v := &Session{}
	return v, sc.Scan(&v.ID, &v.Code, &v.State, jsonCol{&v.Capabilities}, &v.EnvFile, &v.Unverified, &v.UpdatedAt)
}

// UpdateSession 在一个事务中读取会话、交给 f 修改后写回, 并刷新活跃时间.
// 会话不存在时以 id 新建, 初始状态为 idle, 并分配一个 4 位短码.
func (s *Store) UpdateSession(id string, f func(*Session)) (*Session, error) {
	var sess *Session
	err := s.tx(func(tx *sql.Tx) error {
		var err error
		sess, err = one(tx, scanSession, selectSession+"WHERE id = ?", id)
		created := errors.Is(err, ErrNotFound)
		if created {
			sess, err = &Session{ID: id, State: "idle"}, nil
		}
		if err != nil {
			return err
		}
		f(sess)
		sess.UpdatedAt = now()
		if !created {
			_, err = tx.Exec(`UPDATE sessions SET state = ?, capabilities = ?, env_file = ?, unverified = ?, updated_at = ? WHERE id = ?`,
				sess.State, jsonCol{sess.Capabilities}, sess.EnvFile, sess.Unverified, sess.UpdatedAt, id)
			return err
		}
		// 短码撞上已有会话时 INSERT OR IGNORE 不插入, 换一个重试.
		for n := int64(0); n == 0; {
			sess.Code = strings.ToLower(rand.Text()[:4])
			res, err := tx.Exec(`INSERT OR IGNORE INTO sessions (id, code, state, capabilities, env_file, unverified, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				id, sess.Code, sess.State, jsonCol{sess.Capabilities}, sess.EnvFile, sess.Unverified, sess.UpdatedAt)
			if err != nil {
				return err
			}
			n, _ = res.RowsAffected()
		}
		return nil
	})
	return sess, err
}

// Session 按 ID 或短码查找会话.
func (s *Store) Session(idOrCode string) (*Session, error) {
	return one(s.db, scanSession, selectSession+"WHERE id = ? OR code = ?", idOrCode, idOrCode)
}

// Sessions 按活跃时间倒序列出最近 24 小时内活跃的会话.
func (s *Store) Sessions() ([]*Session, error) {
	return list(s.db, scanSession, selectSession+"WHERE updated_at >= ? ORDER BY updated_at DESC",
		time.Now().Add(-sessionActive).UnixMilli())
}

const selectBinding = `SELECT bind_id, session_id, branch, doc_id, sections, base, preserve FROM bindings `

func scanBinding(sc scanner) (*Binding, error) {
	b := &Binding{}
	return b, sc.Scan(&b.ID, &b.SessionID, &b.Branch, &b.DocID, jsonCol{&b.Sections}, &b.Base, &b.Preserve)
}

// AddBindings 在一个事务中记录一次 bind 涉及的全部文档, 返回它们共用的 ID "b-<首行序号>".
// 关联了会话时, 该会话此前对同一文档的绑定随之失效, 以最新一次为准.
func (s *Store) AddBindings(bs []*Binding) (string, error) {
	var id string
	err := s.tx(func(tx *sql.Tx) error {
		for _, b := range bs {
			res, err := tx.Exec(`INSERT INTO bindings (bind_id, session_id, branch, doc_id, sections, base, preserve, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				id, b.SessionID, b.Branch, b.DocID, jsonCol{b.Sections}, b.Base, b.Preserve, now())
			if err != nil {
				return err
			}
			if id == "" {
				seq, _ := res.LastInsertId()
				id = "b-" + strconv.FormatInt(seq, 10)
				if _, err := tx.Exec(`UPDATE bindings SET bind_id = ? WHERE seq = ?`, id, seq); err != nil {
					return err
				}
			}
		}
		return supersede(tx, id)
	})
	return id, err
}

// supersede 使一次 bind 所属会话此前对同一文档的其他有效绑定失效; 未关联会话的绑定不受影响.
func supersede(tx *sql.Tx, bindID string) error {
	_, err := tx.Exec(`UPDATE bindings SET stale = 1 WHERE NOT stale AND bind_id <> ?1 AND (session_id, doc_id) IN
		(SELECT session_id, doc_id FROM bindings WHERE bind_id = ?1 AND session_id <> '' AND NOT stale)`, bindID)
	return err
}

// Bindings 按创建顺序返回会话的有效绑定.
func (s *Store) Bindings(sessionID string) ([]*Binding, error) {
	return list(s.db, scanBinding, selectBinding+"WHERE session_id = ? AND NOT stale ORDER BY seq", sessionID)
}

// ActiveBindings 按创建顺序返回分支上已关联会话的全部有效绑定.
// 宿主崩溃时收不到会话结束, 与会话列表一致, 会话 (未登记时按绑定本身) 超过 24 小时没有活动的绑定不再计入.
func (s *Store) ActiveBindings(branch string) ([]*Binding, error) {
	return list(s.db, scanBinding, selectBinding+`LEFT JOIN sessions ON sessions.id = session_id
WHERE branch = ? AND session_id <> '' AND NOT stale AND COALESCE(sessions.updated_at, created_at) >= ? ORDER BY seq`,
		branch, time.Now().Add(-sessionActive).UnixMilli())
}

// AttachBinding 把一次 bind 中尚未关联会话的绑定关联到会话, 供 after-tool 兜底; 会话此前对同一文档的绑定随之失效.
// 已关联到同一会话时视为成功; 绑定不存在、已失效或已属于其他会话时返回 ErrNotFound.
func (s *Store) AttachBinding(bindID, sessionID string) error {
	return s.tx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE bindings SET session_id = ? WHERE bind_id = ? AND session_id IN ('', ?) AND NOT stale`, sessionID, bindID, sessionID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return supersede(tx, bindID)
	})
}

// AdvanceBinding 把会话对文档的有效绑定基准推进到 base, 报告是否有这样的绑定.
func (s *Store) AdvanceBinding(sessionID, docID, base string) (bool, error) {
	res, err := s.db.Exec(`UPDATE bindings SET base = ? WHERE session_id = ? AND doc_id = ? AND NOT stale`, base, sessionID, docID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ReleaseBindings 在会话结束时使其全部绑定失效.
func (s *Store) ReleaseBindings(sessionID string) error {
	_, err := s.db.Exec(`UPDATE bindings SET stale = 1 WHERE session_id = ? AND NOT stale`, sessionID)
	return err
}

// InvalidateBindings 把不属于 branch 的绑定标记为失效.
func (s *Store) InvalidateBindings(branch string) error {
	_, err := s.db.Exec(`UPDATE bindings SET stale = 1 WHERE branch <> ? AND NOT stale`, branch)
	return err
}

// Turn 返回会话当前回合的状态, 不存在时返回空回合.
func (s *Store) Turn(sessionID string) (*Turn, error) {
	return readTurn(s.db, sessionID)
}

// UpdateTurn 在一个事务中读取会话当前回合的状态、交给 f 修改后写回, 返回修改后的回合.
// f 在写事务中执行, 不能再访问存储.
func (s *Store) UpdateTurn(sessionID string, f func(*Turn)) (*Turn, error) {
	var t *Turn
	err := s.tx(func(tx *sql.Tx) error {
		var err error
		if t, err = readTurn(tx, sessionID); err != nil {
			return err
		}
		f(t)
		_, err = tx.Exec(`REPLACE INTO turns (session_id, data) VALUES (?, ?)`, sessionID, jsonCol{t})
		return err
	})
	return t, err
}

func readTurn(q querier, sessionID string) (*Turn, error) {
	t := &Turn{}
	err := q.QueryRow(`SELECT data FROM turns WHERE session_id = ?`, sessionID).Scan(jsonCol{t})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	t.SessionID = sessionID
	return t, nil
}

// ResetTurn 在新回合开始时清空会话的回合状态.
func (s *Store) ResetTurn(sessionID string) error {
	_, err := s.db.Exec(`DELETE FROM turns WHERE session_id = ?`, sessionID)
	return err
}

// ---------- 跳过原因 ----------

// AddSkip 登记一条待写入提交信息的 Pair-Skip 原因, 相同原因不重复登记.
func (s *Store) AddSkip(reason, sessionID string) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO skips (reason, session_id) VALUES (?, ?)`, reason, sessionID)
	return err
}

// PendingSkips 按登记顺序返回尚未消费的跳过原因.
func (s *Store) PendingSkips() ([]string, error) {
	return list(s.db, scanString, `SELECT reason FROM skips ORDER BY rowid`)
}

// ConsumeSkips 清除已写入提交信息的跳过原因; 其余原因 (如编辑提交信息期间新登记的) 留待下次提交.
func (s *Store) ConsumeSkips(reasons []string) error {
	_, err := s.db.Exec(`DELETE FROM skips WHERE reason IN (SELECT value FROM json_each(?))`, jsonCol{reasons})
	return err
}

// ---------- 事件 ----------

// AppendEvent 分配递增序号与时间后持久化事件, 并只保留最近 10000 条. 返回带序号与时间的副本.
func (s *Store) AppendEvent(e *pairv1.Event) (*pairv1.Event, error) {
	e = proto.CloneOf(e)
	e.Seq, e.At = 0, now()
	data, err := protojson.Marshal(e)
	if err != nil {
		return nil, err
	}
	err = s.tx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO events (data) VALUES (?)`, data)
		if err != nil {
			return err
		}
		e.Seq, _ = res.LastInsertId()
		_, err = tx.Exec(`DELETE FROM events WHERE seq <= ?`, e.Seq-eventKeep)
		return err
	})
	if err != nil {
		return nil, err
	}
	return e, nil
}

// LastSeq 返回最新的事件序号, 没有事件时为 0.
func (s *Store) LastSeq() (int64, error) {
	var seq int64
	err := s.db.QueryRow(`SELECT COALESCE(MAX(seq), 0) FROM events`).Scan(&seq)
	return seq, err
}

// EventsSince 返回序号大于 seq 的事件, 按序号升序, 最多 limit 条; limit <= 0 时不限条数.
func (s *Store) EventsSince(seq int64, limit int) ([]*pairv1.Event, error) {
	if limit <= 0 {
		limit = -1
	}
	return list(s.db, scanEvent, `SELECT seq, data FROM events WHERE seq > ? ORDER BY seq LIMIT ?`, seq, limit)
}

// scanEvent 读取事件; 序号以列为准, payload 写入时尚未分配序号.
func scanEvent(sc scanner) (*pairv1.Event, error) {
	var seq int64
	var data []byte
	if err := sc.Scan(&seq, &data); err != nil {
		return nil, err
	}
	e := &pairv1.Event{}
	if err := protojson.Unmarshal(data, e); err != nil {
		return nil, err
	}
	e.Seq = seq
	return e, nil
}
