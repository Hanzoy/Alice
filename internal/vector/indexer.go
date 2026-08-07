package vector

import (
	"context"
	"sync"
	"time"

	"alice/internal/facts"
	"alice/internal/storage"
)

type Indexer struct {
	db      *storage.DB
	facts   *facts.Store
	vectors *Store
	stop    chan struct{}
	done    chan struct{}
	once    sync.Once
}

func NewIndexer(db *storage.DB, factStore *facts.Store, vectors *Store) *Indexer {
	return &Indexer{db: db, facts: factStore, vectors: vectors, stop: make(chan struct{}), done: make(chan struct{})}
}
func (i *Indexer) Start() {
	go func() {
		defer close(i.done)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		i.flush()
		for {
			select {
			case <-ticker.C:
				i.flush()
			case <-i.stop:
				return
			}
		}
	}()
}
func (i *Indexer) Close() { i.once.Do(func() { close(i.stop); <-i.done }) }
func (i *Indexer) flush() {
	if i == nil || i.db == nil || i.vectors == nil || !i.vectors.embedder.EmbeddingConfigured() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rows, err := i.db.Pool.Query(ctx, `SELECT fact_id FROM vector_outbox WHERE next_attempt_at<=now() ORDER BY created_at LIMIT 50`)
	if err != nil {
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	items, err := i.facts.ByIDs(ctx, ids)
	if err != nil {
		return
	}
	for _, fact := range items {
		if err := i.vectors.Upsert(ctx, fact.ID, fact.EmbeddingText()); err != nil {
			_, _ = i.db.Pool.Exec(ctx, `UPDATE vector_outbox SET attempts=attempts+1,last_error=$2,next_attempt_at=now()+interval '30 seconds' WHERE fact_id=$1`, fact.ID, err.Error())
			continue
		}
		_, _ = i.db.Pool.Exec(ctx, `DELETE FROM vector_outbox WHERE fact_id=$1`, fact.ID)
	}
	var pending int64
	if i.db.Pool.QueryRow(ctx, `SELECT count(*) FROM vector_outbox`).Scan(&pending) == nil && pending == 0 {
		_ = i.vectors.ActivateTarget(ctx)
	}
}
