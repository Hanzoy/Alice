package memory

import (
	"alice/internal/facts"
	"context"
	"testing"
)

func TestRetrieverFallsBackToFactStoreWithoutVectorService(t *testing.T) {
	store := facts.NewMemoryStore()
	_, _, _ = store.Add(facts.Fact{Subject: "user", Predicate: "dislikes", Object: "香菜"})
	r := &Retriever{Facts: store}
	hits, err := r.Search(context.Background(), "香菜", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Object != "香菜" {
		t.Fatalf("unexpected hits: %+v", hits)
	}
}
