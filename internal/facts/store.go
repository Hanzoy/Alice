package facts

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Fact struct {
	ID          string            `json:"id"`
	Subject     string            `json:"subject"`
	Predicate   string            `json:"predicate"`
	Object      string            `json:"object"`
	Qualifiers  map[string]string `json:"qualifiers,omitempty"`
	AssertedBy  string            `json:"asserted_by,omitempty"`
	SourceKind  string            `json:"source_kind"`
	SourceIDs   []string          `json:"source_ids,omitempty"`
	Confidence  float64           `json:"confidence"`
	ValidFrom   int64             `json:"valid_from,omitempty"`
	ValidUntil  int64             `json:"valid_until,omitempty"`
	Sensitivity string            `json:"sensitivity,omitempty"`
	Status      string            `json:"status"`
	Supersedes  string            `json:"supersedes,omitempty"`
	Tags        []string          `json:"tags,omitempty"`
	CreatedAt   int64             `json:"created_at"`
	UpdatedAt   int64             `json:"updated_at"`
}

func (f Fact) Text() string { return strings.TrimSpace(f.Subject + " " + f.Predicate + " " + f.Object) }
func (f Fact) EmbeddingText() string {
	var b strings.Builder
	fmt.Fprintf(&b, "主体：%s；关系：%s；内容：%s", f.Subject, f.Predicate, f.Object)
	if len(f.Qualifiers) > 0 {
		keys := make([]string, 0, len(f.Qualifiers))
		for k := range f.Qualifiers {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString("；限定：")
		for i, k := range keys {
			if i > 0 {
				b.WriteString("，")
			}
			fmt.Fprintf(&b, "%s=%s", k, f.Qualifiers[k])
		}
	}
	if len(f.Tags) > 0 {
		b.WriteString("；标签：" + strings.Join(f.Tags, "，"))
	}
	return b.String()
}

type Store struct {
	pool     *pgxpool.Pool
	mu       sync.RWMutex
	memory   []Fact
	OnCommit func(context.Context, Fact) error
}

func NewPostgresStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }
func NewMemoryStore() *Store                     { return &Store{} }

func (s *Store) ImportJSON(ctx context.Context, path string) (int, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var legacy []Fact
	if err = json.Unmarshal(b, &legacy); err != nil {
		return 0, fmt.Errorf("decode legacy facts: %w", err)
	}
	count := 0
	for _, f := range legacy {
		_, created, addErr := s.AddContext(ctx, f, "coexist")
		if addErr != nil {
			return count, addErr
		}
		if created {
			count++
		}
	}
	return count, nil
}

func (s *Store) Add(f Fact) (Fact, bool, error) {
	return s.AddContext(context.Background(), f, "coexist")
}

func (s *Store) AddContext(ctx context.Context, f Fact, strategy string) (Fact, bool, error) {
	normalize(&f)
	if s.pool == nil {
		return s.addMemory(ctx, f, strategy)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Fact{}, false, err
	}
	defer tx.Rollback(ctx)
	var existing Fact
	err = scanFact(tx.QueryRow(ctx, factSelect+` WHERE f.status='active' AND f.subject=$1 AND f.predicate=$2 AND f.object=$3 LIMIT 1`, f.Subject, f.Predicate, f.Object), &existing)
	if err == nil {
		return existing, false, nil
	}
	if err != pgx.ErrNoRows {
		return Fact{}, false, err
	}
	if strategy == "replace" || strategy == "supersede" {
		var prior string
		err = tx.QueryRow(ctx, `SELECT id FROM facts WHERE status='active' AND subject=$1 AND predicate=$2 ORDER BY updated_at DESC LIMIT 1 FOR UPDATE`, f.Subject, f.Predicate).Scan(&prior)
		if err == nil {
			f.Supersedes = prior
			if _, err = tx.Exec(ctx, `UPDATE facts SET status='superseded',updated_at=now() WHERE id=$1`, prior); err != nil {
				return Fact{}, false, err
			}
		} else if err != pgx.ErrNoRows {
			return Fact{}, false, err
		}
	}
	qualifiers, _ := json.Marshal(f.Qualifiers)
	tags, _ := json.Marshal(f.Tags)
	_, err = tx.Exec(ctx, `INSERT INTO facts(id,subject,predicate,object,qualifiers,asserted_by,source_kind,confidence,valid_from,valid_until,sensitivity,status,supersedes,tags,created_at,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,to_timestamp($15::double precision/1000),to_timestamp($16::double precision/1000))`,
		f.ID, f.Subject, f.Predicate, f.Object, qualifiers, f.AssertedBy, f.SourceKind, f.Confidence, f.ValidFrom, f.ValidUntil, f.Sensitivity, f.Status, f.Supersedes, tags, f.CreatedAt, f.UpdatedAt)
	if err != nil {
		return Fact{}, false, err
	}
	for _, sourceID := range f.SourceIDs {
		if strings.TrimSpace(sourceID) != "" {
			if _, err = tx.Exec(ctx, `INSERT INTO fact_sources(fact_id,source_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, f.ID, sourceID); err != nil {
				return Fact{}, false, err
			}
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO vector_outbox(fact_id) VALUES($1) ON CONFLICT(fact_id) DO UPDATE SET action='upsert',next_attempt_at=now()`, f.ID); err != nil {
		return Fact{}, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Fact{}, false, err
	}
	if s.OnCommit != nil {
		if err = s.OnCommit(ctx, f); err == nil {
			_, _ = s.pool.Exec(ctx, `DELETE FROM vector_outbox WHERE fact_id=$1`, f.ID)
		}
	}
	return f, true, nil
}

func (s *Store) List() []Fact { out, _ := s.ListContext(context.Background(), 500); return out }
func (s *Store) ListContext(ctx context.Context, limit int) ([]Fact, error) {
	if limit <= 0 {
		limit = 500
	}
	if s.pool == nil {
		s.mu.RLock()
		defer s.mu.RUnlock()
		out := append([]Fact(nil), s.memory...)
		sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
		if len(out) > limit {
			out = out[:limit]
		}
		return out, nil
	}
	rows, err := s.pool.Query(ctx, factSelect+` ORDER BY f.updated_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectFacts(rows)
}

func (s *Store) Search(query string, limit int) []Fact {
	out, _ := s.SearchContext(context.Background(), query, limit)
	return out
}
func (s *Store) SearchContext(ctx context.Context, query string, limit int) ([]Fact, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 5
	}
	if s.pool == nil {
		return s.searchMemory(query, limit), nil
	}
	rows, err := s.pool.Query(ctx, factSelect+` WHERE f.status='active' AND (f.search_text ILIKE '%' || $1 || '%' OR similarity(f.search_text,$1)>0.05)
		ORDER BY CASE WHEN f.search_text ILIKE '%' || $1 || '%' THEN 1 ELSE 0 END DESC, similarity(f.search_text,$1) DESC, f.updated_at DESC LIMIT $2`, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectFacts(rows)
}

func (s *Store) ByIDs(ctx context.Context, ids []string) ([]Fact, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if s.pool == nil {
		s.mu.RLock()
		defer s.mu.RUnlock()
		m := map[string]Fact{}
		for _, f := range s.memory {
			m[f.ID] = f
		}
		out := make([]Fact, 0, len(ids))
		for _, id := range ids {
			if f, ok := m[id]; ok {
				out = append(out, f)
			}
		}
		return out, nil
	}
	rows, err := s.pool.Query(ctx, factSelect+` WHERE f.id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found, err := collectFacts(rows)
	if err != nil {
		return nil, err
	}
	m := map[string]Fact{}
	for _, f := range found {
		m[f.ID] = f
	}
	out := make([]Fact, 0, len(ids))
	for _, id := range ids {
		if f, ok := m[id]; ok {
			out = append(out, f)
		}
	}
	return out, nil
}

const factSelect = `SELECT f.id,f.subject,f.predicate,f.object,f.qualifiers,f.asserted_by,f.source_kind,
	f.confidence,f.valid_from,f.valid_until,f.sensitivity,f.status,f.supersedes,f.tags,
	(extract(epoch from f.created_at)*1000)::bigint,(extract(epoch from f.updated_at)*1000)::bigint,
	COALESCE((SELECT array_agg(fs.source_id ORDER BY fs.source_id) FROM fact_sources fs WHERE fs.fact_id=f.id),ARRAY[]::text[]) FROM facts f`

type rowScanner interface{ Scan(...any) error }

func scanFact(row rowScanner, f *Fact) error {
	var q, t []byte
	err := row.Scan(&f.ID, &f.Subject, &f.Predicate, &f.Object, &q, &f.AssertedBy, &f.SourceKind, &f.Confidence, &f.ValidFrom, &f.ValidUntil, &f.Sensitivity, &f.Status, &f.Supersedes, &t, &f.CreatedAt, &f.UpdatedAt, &f.SourceIDs)
	if err == nil {
		_ = json.Unmarshal(q, &f.Qualifiers)
		_ = json.Unmarshal(t, &f.Tags)
	}
	return err
}

type rowsScanner interface {
	Next() bool
	Scan(...any) error
	Err() error
}

func collectFacts(rows rowsScanner) ([]Fact, error) {
	var out []Fact
	for rows.Next() {
		var f Fact
		if err := scanFact(rows, &f); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func normalize(f *Fact) {
	now := time.Now().UnixMilli()
	if f.ID == "" {
		f.ID = factID()
	}
	if f.Status == "" {
		f.Status = "active"
	}
	if f.SourceKind == "" {
		f.SourceKind = "explicit_statement"
	}
	if f.Confidence == 0 {
		f.Confidence = 1
	}
	if f.Sensitivity == "" {
		f.Sensitivity = "normal"
	}
	if f.CreatedAt == 0 {
		f.CreatedAt = now
	}
	f.UpdatedAt = now
	if f.Qualifiers == nil {
		f.Qualifiers = map[string]string{}
	}
	if f.Tags == nil {
		f.Tags = []string{}
	}
}

func (s *Store) addMemory(ctx context.Context, f Fact, strategy string) (Fact, bool, error) {
	s.mu.Lock()
	for _, e := range s.memory {
		if e.Status == "active" && e.Subject == f.Subject && e.Predicate == f.Predicate && e.Object == f.Object {
			s.mu.Unlock()
			return e, false, nil
		}
	}
	if strategy == "replace" || strategy == "supersede" {
		for i := range s.memory {
			if s.memory[i].Status == "active" && s.memory[i].Subject == f.Subject && s.memory[i].Predicate == f.Predicate {
				f.Supersedes = s.memory[i].ID
				s.memory[i].Status = "superseded"
				s.memory[i].UpdatedAt = time.Now().UnixMilli()
			}
		}
	}
	s.memory = append(s.memory, f)
	s.mu.Unlock()
	if s.OnCommit != nil {
		if err := s.OnCommit(ctx, f); err != nil {
			return f, true, err
		}
	}
	return f, true, nil
}
func (s *Store) searchMemory(q string, limit int) []Fact {
	s.mu.RLock()
	defer s.mu.RUnlock()
	q = strings.ToLower(q)
	var out []Fact
	for _, f := range s.memory {
		if f.Status == "active" && strings.Contains(strings.ToLower(f.Text()), q) {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func factID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("fact_%d", time.Now().UnixNano())
	}
	return "fact_" + hex.EncodeToString(b)
}
