package store

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// migrations 按顺序执行, PRAGMA user_version 记录已执行的条数.
var migrations = []string{`
CREATE TABLE feedback (
	seq INTEGER PRIMARY KEY AUTOINCREMENT,
	id TEXT NOT NULL,
	kind INTEGER NOT NULL,
	state INTEGER NOT NULL,
	branch TEXT NOT NULL,
	pick TEXT NOT NULL DEFAULT '',
	note TEXT NOT NULL DEFAULT '',
	target TEXT NOT NULL DEFAULT '',
	assumption_id TEXT NOT NULL DEFAULT '',
	assumption_text TEXT NOT NULL DEFAULT '',
	targets TEXT NOT NULL DEFAULT 'null',
	progress TEXT NOT NULL DEFAULT 'null',
	steer INTEGER NOT NULL DEFAULT 0,
	drained_by TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	triggered_at INTEGER NOT NULL DEFAULT 0,
	drained_at INTEGER NOT NULL DEFAULT 0,
	resolved_at INTEGER NOT NULL DEFAULT 0
);
-- 同一分支同一 ID 同时只有一条未闭环 (state <> RESOLVED) 条目, 已闭环的历史条目保留.
CREATE UNIQUE INDEX feedback_open ON feedback (branch, id) WHERE state <> 4;

CREATE TABLE sessions (
	id TEXT PRIMARY KEY,
	code TEXT NOT NULL UNIQUE,
	state TEXT NOT NULL,
	capabilities TEXT NOT NULL,
	updated_at INTEGER NOT NULL
);

CREATE TABLE bindings (
	seq INTEGER PRIMARY KEY AUTOINCREMENT,
	session_id TEXT NOT NULL,
	branch TEXT NOT NULL,
	doc_id TEXT NOT NULL,
	sections TEXT NOT NULL,
	base TEXT NOT NULL,
	preserve TEXT NOT NULL,
	stale INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL
);

CREATE TABLE turns (
	session_id TEXT PRIMARY KEY,
	data TEXT NOT NULL
);

CREATE TABLE skips (
	reason TEXT PRIMARY KEY,
	session_id TEXT NOT NULL
);

CREATE TABLE events (
	seq INTEGER PRIMARY KEY AUTOINCREMENT,
	data BLOB NOT NULL
);
`}

func migrate(tx *sql.Tx) error {
	var v int
	if err := tx.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	for ; v < len(migrations); v++ {
		if _, err := tx.Exec(migrations[v]); err != nil {
			return fmt.Errorf("迁移到版本 %d: %w", v+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, v+1)); err != nil {
			return err
		}
	}
	return nil
}

// tx 在一个 IMMEDIATE 事务中执行 fn, fn 返回错误时回滚.
func (s *Store) tx(fn func(*sql.Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// execOne 执行应当影响一行的写操作, 未影响任何行时返回 ErrNotFound.
func (s *Store) execOne(query string, args ...any) error {
	res, err := s.db.Exec(query, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// querier 是 *sql.DB 与 *sql.Tx 的公共查询能力.
type querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// scanner 是 *sql.Row 与 *sql.Rows 的公共读取能力.
type scanner interface {
	Scan(dest ...any) error
}

// list 执行查询并用 scan 读取每一行.
func list[T any](q querier, scan func(scanner) (T, error), query string, args ...any) ([]T, error) {
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// one 执行查询并读取首行, 无结果时返回 ErrNotFound.
func one[T any](q querier, scan func(scanner) (T, error), query string, args ...any) (T, error) {
	v, err := scan(q.QueryRow(query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		var zero T
		return zero, ErrNotFound
	}
	return v, err
}

func scanInt(sc scanner) (int64, error) {
	var n int64
	err := sc.Scan(&n)
	return n, err
}

func scanString(sc scanner) (string, error) {
	var v string
	err := sc.Scan(&v)
	return v, err
}

// jsonCol 以 JSON 文本存取 Go 值: 作参数时 v 是值, 作 Scan 目标时 v 是指针.
type jsonCol struct{ v any }

func (j jsonCol) Value() (driver.Value, error) {
	b, err := json.Marshal(j.v)
	return string(b), err
}

func (j jsonCol) Scan(src any) error {
	return json.Unmarshal([]byte(src.(string)), j.v)
}

func now() int64 { return time.Now().UnixMilli() }
