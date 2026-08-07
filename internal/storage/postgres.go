package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"alice/internal/core"
	"github.com/jackc/pgx/v5/pgxpool"
)

type DB struct {
	Pool *pgxpool.Pool
	URL  string
}

type Message struct {
	ID          string `json:"id"`
	Role        string `json:"role"`
	Source      string `json:"source"`
	ReplyHandle string `json:"reply_handle,omitempty"`
	Content     string `json:"content"`
	InputID     string `json:"input_id,omitempty"`
	ExecutionID string `json:"execution_id,omitempty"`
	CreatedAt   int64  `json:"created_at"`
}

func Open(ctx context.Context, databaseURL string) (*DB, error) {
	if databaseURL == "" {
		return nil, fmt.Errorf("ALICE_DATABASE_URL is required")
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse PostgreSQL URL: %w", err)
	}
	config.MaxConns = 10
	config.MinConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("open PostgreSQL: %w", err)
	}
	db := &DB{Pool: pool, URL: databaseURL}
	deadline := time.Now().Add(30 * time.Second)
	for {
		err = pool.Ping(ctx)
		if err == nil || time.Now().After(deadline) || ctx.Err() != nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect PostgreSQL: %w", err)
	}
	if err := db.Migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return db, nil
}

func (d *DB) Close() {
	if d != nil && d.Pool != nil {
		d.Pool.Close()
	}
}

func (d *DB) Migrate(ctx context.Context) error {
	statements := []string{
		`CREATE EXTENSION IF NOT EXISTS pg_trgm`,
		`CREATE TABLE IF NOT EXISTS alice_schema_migrations (version integer PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`,
		`CREATE TABLE IF NOT EXISTS settings (
			key text PRIMARY KEY, value jsonb NOT NULL, updated_at timestamptz NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS messages (
			id text PRIMARY KEY, role text NOT NULL, source text NOT NULL, reply_handle text NOT NULL DEFAULT '',
			content text NOT NULL, input_id text NOT NULL DEFAULT '', execution_id text NOT NULL DEFAULT '',
			created_at timestamptz NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS messages_created_at_idx ON messages (created_at DESC)`,
		`CREATE TABLE IF NOT EXISTS facts (
			id text PRIMARY KEY, subject text NOT NULL, predicate text NOT NULL, object text NOT NULL,
			qualifiers jsonb NOT NULL DEFAULT '{}'::jsonb, asserted_by text NOT NULL DEFAULT '',
			source_kind text NOT NULL, confidence double precision NOT NULL DEFAULT 1,
			valid_from bigint NOT NULL DEFAULT 0, valid_until bigint NOT NULL DEFAULT 0,
			sensitivity text NOT NULL DEFAULT 'normal', status text NOT NULL DEFAULT 'active',
			supersedes text NOT NULL DEFAULT '', tags jsonb NOT NULL DEFAULT '[]'::jsonb,
			search_text text GENERATED ALWAYS AS (subject || ' ' || predicate || ' ' || object) STORED,
			created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS facts_active_exact_idx ON facts(subject, predicate, object) WHERE status = 'active'`,
		`CREATE INDEX IF NOT EXISTS facts_search_trgm_idx ON facts USING gin(search_text gin_trgm_ops)`,
		`CREATE INDEX IF NOT EXISTS facts_relation_idx ON facts(subject, predicate, status)`,
		`CREATE TABLE IF NOT EXISTS fact_sources (
			fact_id text NOT NULL REFERENCES facts(id) ON DELETE CASCADE, source_id text NOT NULL,
			PRIMARY KEY(fact_id, source_id)
		)`,
		`CREATE TABLE IF NOT EXISTS vector_outbox (
			fact_id text PRIMARY KEY REFERENCES facts(id) ON DELETE CASCADE,
			action text NOT NULL DEFAULT 'upsert', attempts integer NOT NULL DEFAULT 0,
			last_error text NOT NULL DEFAULT '', next_attempt_at timestamptz NOT NULL DEFAULT now(),
			created_at timestamptz NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS executions (
			id text PRIMARY KEY, blueprint_id text NOT NULL, blueprint_version integer NOT NULL,
			status text NOT NULL, source text NOT NULL, created_at bigint NOT NULL,
			started_at bigint NOT NULL DEFAULT 0, finished_at bigint NOT NULL DEFAULT 0,
			error text NOT NULL DEFAULT '', snapshot jsonb NOT NULL, updated_at timestamptz NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS executions_created_at_idx ON executions(created_at DESC)`,
	}
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin migration: %w", err)
	}
	defer tx.Rollback(ctx)
	for _, statement := range statements {
		if _, err := tx.Exec(ctx, statement); err != nil {
			return fmt.Errorf("migrate PostgreSQL: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO alice_schema_migrations(version) VALUES(1) ON CONFLICT DO NOTHING`); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (d *DB) Status(ctx context.Context) map[string]any {
	status := map[string]any{"driver": "postgresql", "connected": false}
	if d == nil || d.Pool == nil {
		return status
	}
	if err := d.Pool.Ping(ctx); err != nil {
		status["error"] = err.Error()
		return status
	}
	status["connected"] = true
	var messages, facts, executions, vectorPending int64
	_ = d.Pool.QueryRow(ctx, `SELECT count(*) FROM messages`).Scan(&messages)
	_ = d.Pool.QueryRow(ctx, `SELECT count(*) FROM facts`).Scan(&facts)
	_ = d.Pool.QueryRow(ctx, `SELECT count(*) FROM executions`).Scan(&executions)
	_ = d.Pool.QueryRow(ctx, `SELECT count(*) FROM vector_outbox`).Scan(&vectorPending)
	status["messages"] = messages
	status["facts"] = facts
	status["executions"] = executions
	status["vector_pending"] = vectorPending
	return status
}

func (d *DB) AddMessage(ctx context.Context, m Message) error {
	if m.CreatedAt == 0 {
		m.CreatedAt = time.Now().UnixMilli()
	}
	_, err := d.Pool.Exec(ctx, `INSERT INTO messages(id,role,source,reply_handle,content,input_id,execution_id,created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,to_timestamp($8::double precision/1000)) ON CONFLICT(id) DO NOTHING`,
		m.ID, m.Role, m.Source, m.ReplyHandle, m.Content, m.InputID, m.ExecutionID, m.CreatedAt)
	return err
}

func (d *DB) RecentMessages(ctx context.Context, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := d.Pool.Query(ctx, `SELECT id,role,source,reply_handle,content,input_id,execution_id,(extract(epoch from created_at)*1000)::bigint
		FROM messages ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.Role, &m.Source, &m.ReplyHandle, &m.Content, &m.InputID, &m.ExecutionID, &m.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	return result, rows.Err()
}

func (d *DB) PutSetting(ctx context.Context, key string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = d.Pool.Exec(ctx, `INSERT INTO settings(key,value) VALUES($1,$2) ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=now()`, key, b)
	return err
}

func (d *DB) QueueAllFactsForVector(ctx context.Context) error {
	_, err := d.Pool.Exec(ctx, `INSERT INTO vector_outbox(fact_id) SELECT id FROM facts WHERE status='active' ON CONFLICT(fact_id) DO UPDATE SET action='upsert',next_attempt_at=now(),last_error=''`)
	return err
}

func (d *DB) GetSetting(ctx context.Context, key string, out any) (bool, error) {
	var raw []byte
	err := d.Pool.QueryRow(ctx, `SELECT value FROM settings WHERE key=$1`, key).Scan(&raw)
	if err != nil {
		if stringsContainsNoRows(err.Error()) {
			return false, nil
		}
		return false, err
	}
	return true, json.Unmarshal(raw, out)
}

func (d *DB) SaveExecution(ctx context.Context, s core.ExecutionSnapshot) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = d.Pool.Exec(ctx, `INSERT INTO executions(id,blueprint_id,blueprint_version,status,source,created_at,started_at,finished_at,error,snapshot)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(id) DO UPDATE SET status=excluded.status,started_at=excluded.started_at,finished_at=excluded.finished_at,error=excluded.error,snapshot=excluded.snapshot,updated_at=now()`,
		s.ID, s.BlueprintID, s.BlueprintVersion, string(s.Status), s.Source, s.CreatedAt, s.StartedAt, s.FinishedAt, s.Error, b)
	return err
}

func (d *DB) ListExecutions(ctx context.Context, limit int) []core.ExecutionSnapshot {
	if limit <= 0 {
		limit = 100
	}
	rows, err := d.Pool.Query(ctx, `SELECT snapshot FROM executions ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []core.ExecutionSnapshot
	for rows.Next() {
		var b []byte
		var s core.ExecutionSnapshot
		if rows.Scan(&b) == nil && json.Unmarshal(b, &s) == nil {
			out = append(out, s)
		}
	}
	return out
}

func stringsContainsNoRows(s string) bool { return s == "no rows in result set" }
