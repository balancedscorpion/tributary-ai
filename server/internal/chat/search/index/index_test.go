package index

import (
	"testing"
	"time"

	"ai-analyst-local/internal/chat/jsondb"
)

func TestPrefixQueryAndFilters(t *testing.T) {
	now := time.Now().UTC()
	d1 := jsondb.SearchDoc{
		ID:        "thread:t1",
		DocKind:   "thread",
		ThreadID:  "t1",
		Status:    "active",
		Title:     "Error Investigation",
		Text:      "panic stacktrace and nil pointer details",
		UpdatedAt: now,
	}
	project := "p1"
	d2 := jsondb.SearchDoc{
		ID:        "artifact:a1",
		DocKind:   "artifact",
		ThreadID:  "t2",
		Status:    "archived",
		ProjectID: &project,
		Title:     "Revenue Sheet",
		Text:      "quarterly revenue summary",
		UpdatedAt: now.Add(-time.Hour),
	}

	idx := New()
	idx.ReplaceAll([]jsondb.SearchDoc{d1, d2})

	hits := idx.Search(Query{Text: "erro", Limit: 10})
	if len(hits) == 0 || hits[0].Doc.ID != d1.ID {
		t.Fatalf("expected prefix query to hit %s first, got %+v", d1.ID, hits)
	}

	hits = idx.Search(Query{Text: "revenue", Status: "archived", ProjectID: "p1", Limit: 10})
	if len(hits) != 1 || hits[0].Doc.ID != d2.ID {
		t.Fatalf("expected archived+project filter to return %s, got %+v", d2.ID, hits)
	}

	hits = idx.Search(Query{Text: "revenue", ThreadIDs: []string{"t1"}, Limit: 10})
	if len(hits) != 0 {
		t.Fatalf("expected thread filter to exclude revenue doc, got %+v", hits)
	}
}

func TestIncrementalUpsertChangesResults(t *testing.T) {
	now := time.Now().UTC()
	doc := jsondb.SearchDoc{
		ID:        "turn:r1",
		DocKind:   "turn",
		ThreadID:  "t1",
		Status:    "active",
		Title:     "First pass",
		Text:      "alpha beta",
		UpdatedAt: now,
	}

	idx := New()
	idx.Upsert(doc)

	hits := idx.Search(Query{Text: "gamma", Limit: 10})
	if len(hits) != 0 {
		t.Fatalf("expected no hits before update, got %+v", hits)
	}

	doc.Text = "alpha gamma"
	doc.UpdatedAt = now.Add(time.Minute)
	idx.Upsert(doc)

	hits = idx.Search(Query{Text: "gamma", Limit: 10})
	if len(hits) != 1 || hits[0].Doc.ID != doc.ID {
		t.Fatalf("expected updated doc to be found by gamma, got %+v", hits)
	}
}
