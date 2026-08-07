package facts

import "testing"

func TestStoreDeduplicatesAndSearchesFacts(t *testing.T) {
	store := NewMemoryStore()
	fact := Fact{Subject: "user", Predicate: "dislikes", Object: "香菜", SourceKind: "explicit_statement", Status: "active"}
	if _, created, err := store.Add(fact); err != nil || !created {
		t.Fatalf("first add: created=%v err=%v", created, err)
	}
	if _, created, err := store.Add(fact); err != nil || created {
		t.Fatalf("duplicate add: created=%v err=%v", created, err)
	}
	hits := store.Search("香菜", 5)
	if len(hits) != 1 || hits[0].Object != "香菜" {
		t.Fatalf("unexpected search hits: %+v", hits)
	}
}
