package memory

import (
	"context"
	"sort"

	"alice/internal/facts"
	"alice/internal/vector"
)

type Retriever struct {
	Facts   *facts.Store
	Vectors *vector.Store
}

// Search fuses PostgreSQL lexical/relational results with Qdrant semantic
// results using reciprocal-rank fusion. PostgreSQL remains the source of truth.
func (r *Retriever) Search(ctx context.Context, query string, limit int) ([]facts.Fact, error) {
	if limit <= 0 {
		limit = 5
	}
	lexical, err := r.Facts.SearchContext(ctx, query, limit*3)
	if err != nil {
		return nil, err
	}
	var semantic []vector.Hit
	if r.Vectors != nil {
		semantic, _ = r.Vectors.Search(ctx, query, limit*3)
	}
	scores := map[string]float64{}
	const k = 60.0
	for rank, f := range lexical {
		scores[f.ID] += 1 / (k + float64(rank+1))
	}
	for rank, h := range semantic {
		scores[h.FactID] += 1 / (k + float64(rank+1))
	}
	ids := make([]string, 0, len(scores))
	for id := range scores {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if scores[ids[i]] == scores[ids[j]] {
			return ids[i] < ids[j]
		}
		return scores[ids[i]] > scores[ids[j]]
	})
	if len(ids) > limit {
		ids = ids[:limit]
	}
	return r.Facts.ByIDs(ctx, ids)
}
