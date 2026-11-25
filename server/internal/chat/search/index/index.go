package index

import (
	"math"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"ai-analyst-local/internal/chat/jsondb"
)

type Query struct {
	Text        string
	Limit       int
	Status      string
	ProjectID   string
	ThreadIDs   []string
	ArtifactIDs []string
	MessageIDs  []string
	OrderBy     string
	From        *time.Time
	To          *time.Time
}

type Result struct {
	Doc     jsondb.SearchDoc
	Score   float64
	Snippet string
}

type Index struct {
	mu sync.RWMutex

	docs map[string]jsondb.SearchDoc

	docTerms map[string]map[string]int // docID -> token -> tf
	docLen   map[string]int            // docID -> length

	inverted map[string]map[string]int // token -> docID -> tf

	prefix map[string]map[string]struct{} // prefix -> token set

	tokenRefCount map[string]int

	totalDocLen int
}

func New() *Index {
	return &Index{
		docs:          map[string]jsondb.SearchDoc{},
		docTerms:      map[string]map[string]int{},
		docLen:        map[string]int{},
		inverted:      map[string]map[string]int{},
		prefix:        map[string]map[string]struct{}{},
		tokenRefCount: map[string]int{},
	}
}

func (idx *Index) ReplaceAll(docs []jsondb.SearchDoc) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.docs = map[string]jsondb.SearchDoc{}
	idx.docTerms = map[string]map[string]int{}
	idx.docLen = map[string]int{}
	idx.inverted = map[string]map[string]int{}
	idx.prefix = map[string]map[string]struct{}{}
	idx.tokenRefCount = map[string]int{}
	idx.totalDocLen = 0
	for _, doc := range docs {
		idx.upsertNoLock(doc)
	}
}

func (idx *Index) Upsert(doc jsondb.SearchDoc) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.upsertNoLock(doc)
}

func (idx *Index) Delete(docID string) {
	id := strings.TrimSpace(docID)
	if id == "" {
		return
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.removeNoLock(id)
}

func (idx *Index) Get(docID string) (jsondb.SearchDoc, bool) {
	id := strings.TrimSpace(docID)
	if id == "" {
		return jsondb.SearchDoc{}, false
	}
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	doc, ok := idx.docs[id]
	return doc, ok
}

func (idx *Index) AllDocs() []jsondb.SearchDoc {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	out := make([]jsondb.SearchDoc, 0, len(idx.docs))
	for _, d := range idx.docs {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out
}

func (idx *Index) Search(q Query) []Result {
	limit := q.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	threadSet := make(map[string]struct{}, len(q.ThreadIDs))
	artifactSet := make(map[string]struct{}, len(q.ArtifactIDs))
	messageSet := make(map[string]struct{}, len(q.MessageIDs))
	for _, t := range q.ThreadIDs {
		id := strings.TrimSpace(t)
		if id == "" {
			continue
		}
		threadSet[id] = struct{}{}
	}
	for _, id := range q.ArtifactIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		artifactSet[id] = struct{}{}
	}
	for _, id := range q.MessageIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		messageSet[id] = struct{}{}
	}
	orderBy := strings.ToLower(strings.TrimSpace(q.OrderBy))

	idx.mu.RLock()
	defer idx.mu.RUnlock()
	if len(idx.docs) == 0 {
		return nil
	}
	terms := tokenize(q.Text)
	scores := map[string]float64{}
	if len(terms) == 0 {
		for id, doc := range idx.docs {
				if !matchesFilter(doc, q.Status, q.ProjectID, threadSet, artifactSet, messageSet, q.From, q.To) {
				continue
			}
			scores[id] = recencyScore(doc.UpdatedAt)
		}
	} else {
		docCount := float64(len(idx.docs))
		avgDocLen := float64(idx.totalDocLen) / docCount
		if avgDocLen <= 0 {
			avgDocLen = 1
		}
		const (
			k1 = 1.2
			b  = 0.75
		)

		for _, term := range terms {
			matchedTokens := idx.expandTerm(term)
			for _, token := range matchedTokens {
				postings := idx.inverted[token]
				if len(postings) == 0 {
					continue
				}
				df := float64(len(postings))
				idf := math.Log((docCount-df+0.5)/(df+0.5) + 1.0)
				termBoost := 1.0
				if token != term {
					termBoost = 0.8
				}
				for docID, tf := range postings {
					doc := idx.docs[docID]
					if !matchesFilter(doc, q.Status, q.ProjectID, threadSet, artifactSet, messageSet, q.From, q.To) {
						continue
					}
					dl := float64(idx.docLen[docID])
					if dl <= 0 {
						dl = 1
					}
					tfScore := (float64(tf) * (k1 + 1)) / (float64(tf) + k1*(1-b+b*(dl/avgDocLen)))
					scores[docID] += idf * tfScore * termBoost
				}
			}
		}
	}

	out := make([]Result, 0, len(scores))
	for docID, score := range scores {
		doc := idx.docs[docID]
		out = append(out, Result{
			Doc:     doc,
			Score:   score,
			Snippet: makeSnippet(doc.Text, terms),
		})
	}
	if orderBy == "" {
		orderBy = "score_desc"
	}
	sort.Slice(out, func(i, j int) bool {
		switch orderBy {
		case "updated_at_asc":
			if out[i].Doc.UpdatedAt.Equal(out[j].Doc.UpdatedAt) {
				if out[i].Score == out[j].Score {
					return out[i].Doc.ID < out[j].Doc.ID
				}
				return out[i].Doc.ID < out[j].Doc.ID
			}
			return out[i].Doc.UpdatedAt.Before(out[j].Doc.UpdatedAt)
		case "updated_at_desc":
			if out[i].Doc.UpdatedAt.Equal(out[j].Doc.UpdatedAt) {
				if out[i].Score == out[j].Score {
					return out[i].Doc.ID < out[j].Doc.ID
				}
				return out[i].Doc.ID < out[j].Doc.ID
			}
			return out[i].Doc.UpdatedAt.After(out[j].Doc.UpdatedAt)
		default:
			if out[i].Score == out[j].Score {
				if out[i].Doc.UpdatedAt.Equal(out[j].Doc.UpdatedAt) {
					return out[i].Doc.ID < out[j].Doc.ID
				}
				return out[i].Doc.UpdatedAt.After(out[j].Doc.UpdatedAt)
			}
			return out[i].Score > out[j].Score
		}
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (idx *Index) Suggest(text string, limit int) []jsondb.SearchDoc {
	limit = max(limit, 1)
	limit = min(limit, 20)
	tokens := tokenize(text)
	if len(tokens) == 0 {
		return nil
	}
	prefix := tokens[len(tokens)-1]
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	var out []jsondb.SearchDoc
	for _, doc := range idx.docs {
		title := strings.ToLower(doc.Title)
		if strings.Contains(title, prefix) {
			out = append(out, doc)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (idx *Index) upsertNoLock(doc jsondb.SearchDoc) {
	id := strings.TrimSpace(doc.ID)
	if id == "" {
		return
	}
	idx.removeNoLock(id)

	combined := doc.Title + "\n" + doc.Text
	tokens := tokenize(combined)
	tf := map[string]int{}
	for _, token := range tokens {
		tf[token]++
	}

	idx.docs[id] = doc
	idx.docTerms[id] = tf
	idx.docLen[id] = len(tokens)
	idx.totalDocLen += len(tokens)
	for token, freq := range tf {
		postings := idx.inverted[token]
		if postings == nil {
			postings = map[string]int{}
			idx.inverted[token] = postings
		}
		postings[id] = freq
		idx.tokenRefCount[token]++
		if idx.tokenRefCount[token] == 1 {
			idx.addPrefixToken(token)
		}
	}
}

func (idx *Index) removeNoLock(docID string) {
	id := strings.TrimSpace(docID)
	if id == "" {
		return
	}
	tf, ok := idx.docTerms[id]
	if !ok {
		delete(idx.docs, id)
		delete(idx.docLen, id)
		return
	}
	idx.totalDocLen -= idx.docLen[id]
	if idx.totalDocLen < 0 {
		idx.totalDocLen = 0
	}
	for token := range tf {
		postings := idx.inverted[token]
		if postings != nil {
			delete(postings, id)
			if len(postings) == 0 {
				delete(idx.inverted, token)
			}
		}
		if idx.tokenRefCount[token] > 0 {
			idx.tokenRefCount[token]--
			if idx.tokenRefCount[token] == 0 {
				delete(idx.tokenRefCount, token)
				idx.removePrefixToken(token)
			}
		}
	}
	delete(idx.docTerms, id)
	delete(idx.docLen, id)
	delete(idx.docs, id)
}

func (idx *Index) expandTerm(term string) []string {
	term = strings.TrimSpace(strings.ToLower(term))
	if term == "" {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, 8)
	if _, ok := idx.inverted[term]; ok {
		out = append(out, term)
		seen[term] = struct{}{}
	}
	if toks := idx.prefix[term]; len(toks) > 0 {
		for token := range toks {
			if _, ok := seen[token]; ok {
				continue
			}
			out = append(out, token)
			seen[token] = struct{}{}
		}
	}
	return out
}

func (idx *Index) addPrefixToken(token string) {
	maxLen := min(12, len(token))
	for i := 1; i <= maxLen; i++ {
		p := token[:i]
		set := idx.prefix[p]
		if set == nil {
			set = map[string]struct{}{}
			idx.prefix[p] = set
		}
		set[token] = struct{}{}
	}
}

func (idx *Index) removePrefixToken(token string) {
	maxLen := min(12, len(token))
	for i := 1; i <= maxLen; i++ {
		p := token[:i]
		set := idx.prefix[p]
		if set == nil {
			continue
		}
		delete(set, token)
		if len(set) == 0 {
			delete(idx.prefix, p)
		}
	}
}

func tokenize(s string) []string {
	text := strings.ToLower(strings.TrimSpace(s))
	if text == "" {
		return nil
	}
	out := make([]string, 0, 32)
	var cur []rune
	flush := func() {
		if len(cur) == 0 {
			return
		}
		out = append(out, string(cur))
		cur = cur[:0]
	}
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			cur = append(cur, r)
		} else {
			flush()
		}
	}
	flush()
	return out
}

func matchesFilter(doc jsondb.SearchDoc, status string, projectID string, threadSet map[string]struct{}, artifactSet map[string]struct{}, messageSet map[string]struct{}, from *time.Time, to *time.Time) bool {
	st := strings.TrimSpace(status)
	if st == "" {
		st = "active"
	}
	if st != "*" && strings.TrimSpace(doc.Status) != "" && strings.TrimSpace(doc.Status) != st {
		return false
	}
	proj := strings.TrimSpace(projectID)
	if proj != "" {
		if doc.ProjectID == nil || strings.TrimSpace(*doc.ProjectID) != proj {
			return false
		}
	}
	if len(threadSet) > 0 {
		if _, ok := threadSet[doc.ThreadID]; !ok {
			return false
		}
	}
	if len(artifactSet) > 0 {
		if doc.ArtifactID == nil {
			return false
		}
		if _, ok := artifactSet[*doc.ArtifactID]; !ok {
			return false
		}
	}
	if len(messageSet) > 0 {
		if doc.MessageID == nil {
			return false
		}
		if _, ok := messageSet[*doc.MessageID]; !ok {
			return false
		}
	}
	if from != nil && doc.UpdatedAt.Before(*from) {
		return false
	}
	if to != nil && doc.UpdatedAt.After(*to) {
		return false
	}
	return true
}

func makeSnippet(text string, terms []string) string {
	src := strings.TrimSpace(text)
	if src == "" {
		return ""
	}
	if len(src) <= 220 {
		return src
	}
	lower := strings.ToLower(src)
	pos := -1
	for _, term := range terms {
		if term == "" {
			continue
		}
		if p := strings.Index(lower, term); p >= 0 {
			pos = p
			break
		}
	}
	if pos < 0 {
		return src[:220]
	}
	start := max(0, pos-80)
	end := min(len(src), start+220)
	return src[start:end]
}

func recencyScore(t time.Time) float64 {
	if t.IsZero() {
		return 0
	}
	ageHours := time.Since(t).Hours()
	if ageHours < 0 {
		ageHours = 0
	}
	return 1.0 / (1.0 + ageHours/24.0)
}

func min(a int, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a int, b int) int {
	if a > b {
		return a
	}
	return b
}
