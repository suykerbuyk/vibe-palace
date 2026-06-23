// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"fmt"
	"hash/fnv"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// This file is a model-free recall harness salvaged from a sibling HNSW fork.
// It builds a deterministic TF-IDF feature-hashed embedding over a small corpus
// of constitutional documents (no ONNX, no external model), loads them into the
// project's exact brute-force *VectorIndex, and verifies search behavior against
// an independent ground-truth scan that uses identical distance math.

const (
	embeddingDim  = 2048
	chunkMaxChars = 600
)

// chunkText splits text into chunks of roughly maxChars characters,
// breaking at paragraph boundaries (double newline).
func chunkText(text string, maxChars int) []string {
	paragraphs := strings.Split(text, "\n\n")
	var chunks []string
	var current strings.Builder

	for _, p := range paragraphs {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if current.Len() > 0 && current.Len()+len(p)+2 > maxChars {
			chunks = append(chunks, current.String())
			current.Reset()
		}
		if current.Len() > 0 {
			current.WriteString("\n\n")
		}
		current.WriteString(p)
	}
	if current.Len() > 0 {
		chunks = append(chunks, current.String())
	}
	return chunks
}

// wordHash returns a (dimension index, sign) pair for a word.
// This is the "hashing trick" — each word deterministically maps to one
// dimension, so shared words between query and document create direct
// overlap in the vector, unlike random word vectors.
func wordHash(word string, dims int) (int, float32) {
	h := fnv.New64a()
	h.Write([]byte(word))
	hash := h.Sum64()
	idx := int(hash % uint64(dims))
	sign := float32(1.0)
	if hash&(1<<32) != 0 {
		sign = -1.0
	}
	return idx, sign
}

// stopWords are common English words that carry little discriminating value.
// Removing them lets rare, meaningful words dominate the embedding.
var stopWords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true, "at": true,
	"be": true, "by": true, "for": true, "from": true, "has": true, "he": true,
	"in": true, "is": true, "it": true, "its": true, "of": true, "on": true,
	"or": true, "that": true, "the": true, "to": true, "was": true, "were": true,
	"will": true, "with": true, "this": true, "but": true, "they": true,
	"have": true, "had": true, "not": true, "been": true, "no": true,
	"shall": true, "such": true, "any": true, "each": true, "which": true,
	"their": true, "if": true, "than": true, "other": true, "into": true,
	"may": true, "all": true, "who": true, "when": true, "upon": true,
}

// tokenize splits text into lowercase words with stop words removed.
func tokenize(text string) []string {
	raw := strings.Fields(strings.ToLower(text))
	out := make([]string, 0, len(raw))
	for _, w := range raw {
		// Strip common markdown/punctuation from edges
		w = strings.Trim(w, ".,;:!?\"'()[]{}*#>_\\-/")
		if len(w) < 2 || stopWords[w] {
			continue
		}
		out = append(out, w)
	}
	return out
}

// idfTable holds inverse document frequency weights computed from a corpus.
// Words that appear in many documents get low weight; rare words get high weight.
type idfTable struct {
	idf      map[string]float32
	numDocs  int
	defaultW float32 // weight for unseen words (max IDF)
}

// buildIDF computes IDF weights from a set of text chunks.
// IDF(word) = log(N / df(word)) where df is the number of chunks containing the word.
func buildIDF(chunks []string) *idfTable {
	df := make(map[string]int)
	for _, chunk := range chunks {
		seen := make(map[string]bool)
		for _, w := range tokenize(chunk) {
			if !seen[w] {
				df[w]++
				seen[w] = true
			}
		}
	}

	n := float64(len(chunks))
	idf := make(map[string]float32, len(df))
	for word, count := range df {
		idf[word] = float32(math.Log(n / float64(count)))
	}

	return &idfTable{
		idf:      idf,
		numDocs:  len(chunks),
		defaultW: float32(math.Log(n)), // max possible IDF for unseen words
	}
}

func (t *idfTable) weight(word string) float32 {
	if w, ok := t.idf[word]; ok {
		return w
	}
	return t.defaultW
}

// tfidfEmbedding produces a TF-IDF weighted feature-hashed embedding.
// Stop words are removed. Each remaining word hashes to a dimension index
// (the "hashing trick"), weighted by its IDF score. Shared words between
// query and document contribute to the SAME dimensions, creating direct
// cosine similarity signal — unlike random word vectors where shared words
// contribute to unrelated directions.
func tfidfEmbedding(text string, dims int, idf *idfTable) []float32 {
	vec := make([]float32, dims)
	words := tokenize(text)
	if len(words) == 0 {
		return vec
	}

	for _, word := range words {
		w := idf.weight(word)
		idx, sign := wordHash(word, dims)
		vec[idx] += sign * w
	}

	// L2 normalize
	var norm float32
	for _, v := range vec {
		norm += v * v
	}
	norm = float32(math.Sqrt(float64(norm)))
	if norm > 0 {
		for i := range vec {
			vec[i] /= norm
		}
	}
	return vec
}

// loadConstitutionCorpus chunks and embeds every document under
// testdata/constitution/*.md into parallel (ids, vecs) slices, and returns the
// shared *idfTable so cross-document queries can re-embed phrases consistently.
// IDs have the form "DocName:index" (DocName = filename without ".md").
func loadConstitutionCorpus(t *testing.T) (ids []string, vecs [][]float32, idf *idfTable) {
	t.Helper()
	files, err := filepath.Glob("testdata/constitution/*.md")
	if err != nil {
		t.Fatalf("glob testdata: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no test documents found in testdata/constitution/")
	}

	// First pass: chunk every doc, collect chunk texts for the IDF table,
	// and remember each chunk's (docName, index, text).
	type docChunk struct {
		docName string
		index   int
		text    string
	}
	var allChunks []docChunk
	var allTexts []string
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		docName := strings.TrimSuffix(filepath.Base(f), ".md")
		chunks := chunkText(string(data), chunkMaxChars)
		for i, chunk := range chunks {
			allChunks = append(allChunks, docChunk{docName, i, chunk})
			allTexts = append(allTexts, chunk)
		}
	}

	// Build IDF over ALL chunk texts.
	idf = buildIDF(allTexts)

	// Second pass: embed each chunk.
	ids = make([]string, 0, len(allChunks))
	vecs = make([][]float32, 0, len(allChunks))
	for _, dc := range allChunks {
		ids = append(ids, fmt.Sprintf("%s:%d", dc.docName, dc.index))
		vecs = append(vecs, tfidfEmbedding(dc.text, embeddingDim, idf))
	}
	return ids, vecs, idf
}

// buildCorpusIndex inserts the corpus into a fresh *VectorIndex using per-chunk
// Insert (deterministic append order). IDs are unique, so Insert is safe.
func buildCorpusIndex(t *testing.T, ids []string, vecs [][]float32) *VectorIndex {
	t.Helper()
	idx := NewVectorIndex(embeddingDim)
	for i := range ids {
		if err := idx.Insert(ids[i], vecs[i]); err != nil {
			t.Fatalf("Insert %s: %v", ids[i], err)
		}
	}
	return idx
}

// gtThreshold returns the k-th smallest cosineDistanceF32(query, vec) over the
// supplied vectors — the exact distance boundary an exact k-NN search may not
// exceed. It calls the SAME cosineDistanceF32 the index uses, so GT and index
// are bit-identical.
func gtThreshold(query []float32, vecs [][]float32, k int) float32 {
	dists := make([]float32, len(vecs))
	for i, v := range vecs {
		dists[i] = cosineDistanceF32(query, v)
	}
	sort.Slice(dists, func(i, j int) bool { return dists[i] < dists[j] })
	if k > len(dists) {
		k = len(dists)
	}
	return dists[k-1]
}

// docNameFromID extracts the document name from an id like "DocName:42"
// (everything before the last ':').
func docNameFromID(id string) string {
	i := strings.LastIndex(id, ":")
	if i < 0 {
		return id
	}
	return id[:i]
}

// TestConstitutionRecall verifies the index's exact-search behavior against an
// independent cosine scan using a tie-robust distance-bound exactness property.
//
// We assert that every returned distance is <= the k-th smallest distance over
// the full corpus (plus a tiny eps). We deliberately do NOT assert per-rank ID
// equality nor set-overlap recall >= 0.999: the corpus has duplicate structural
// lines producing ties at the k-th boundary, and sort.Slice is not stable, so
// which tied item surfaces is unstable and those assertions would be flaky.
// The distance-bound property is tie-robust and is the real correctness signal.
func TestConstitutionRecall(t *testing.T) {
	ids, vecs, _ := loadConstitutionCorpus(t)
	if len(ids) <= 50 {
		t.Fatalf("expected substantial chunk count, got %d", len(ids))
	}
	idx := buildCorpusIndex(t, ids, vecs)
	if idx.Len() != len(ids) {
		t.Fatalf("index Len = %d, want %d", idx.Len(), len(ids))
	}
	t.Logf("corpus: %d chunks across %d documents", len(ids), 4)

	// eps tolerates float32 truncation between the index's stored distances and
	// the GT scan (both call cosineDistanceF32, but ordering of float ops differs).
	const eps = float32(1e-6)

	n := len(vecs)
	queryIndices := []int{0, n / 4, n / 2, 3 * n / 4, n - 1}

	for _, k := range []int{5, 10, 20} {
		for _, qi := range queryIndices {
			query := vecs[qi]
			got, err := idx.Search(query, k)
			if err != nil {
				t.Fatalf("Search(k=%d, qi=%d): %v", k, qi, err)
			}
			threshold := gtThreshold(query, vecs, k)

			// Exactness property: nothing returned may exceed the GT threshold.
			for i, r := range got {
				if r.Distance > threshold+eps {
					t.Errorf("k=%d qi=%d rank=%d: distance %.9g exceeds GT threshold %.9g (+eps)",
						k, qi, i, r.Distance, threshold)
				}
			}

			// Informational set-overlap recall@k. Meaningful only against a
			// future approximate backend; do not assert on it for this exact index.
			gtIDs := groundTruthIDs(query, ids, vecs, k)
			var hits int
			for _, r := range got {
				if _, ok := gtIDs[r.ID]; ok {
					hits++
				}
			}
			recall := float64(hits) / float64(len(gtIDs))
			t.Logf("recall@%d qi=%d: %.3f", k, qi, recall)
		}
	}
}

// groundTruthIDs returns the id set of the k nearest vectors to query by an
// exhaustive cosine scan (informational metric only).
func groundTruthIDs(query []float32, ids []string, vecs [][]float32, k int) map[string]struct{} {
	type distID struct {
		id   string
		dist float32
	}
	scored := make([]distID, len(vecs))
	for i, v := range vecs {
		scored[i] = distID{ids[i], cosineDistanceF32(query, v)}
	}
	sort.Slice(scored, func(i, j int) bool { return scored[i].dist < scored[j].dist })
	if k > len(scored) {
		k = len(scored)
	}
	out := make(map[string]struct{}, k)
	for i := 0; i < k; i++ {
		out[scored[i].id] = struct{}{}
	}
	return out
}

// TestConstitutionCrossDocumentSearch re-embeds natural-language phrase queries
// with the corpus IDF table and asserts the expected source document appears at
// least once in the top-10 results for each query.
func TestConstitutionCrossDocumentSearch(t *testing.T) {
	ids, vecs, idf := loadConstitutionCorpus(t)
	idx := buildCorpusIndex(t, ids, vecs)

	type phraseQuery struct {
		name   string // subtest name
		phrase string // search query
		srcDoc string // expected source document (filename without .md)
	}

	queries := []phraseQuery{
		// US Constitution
		{"USConst/necessary_and_proper", "necessary and proper", "Full_Text_of_the_U.S._Constitution"},
		{"USConst/electoral_college", "electoral college electors", "Full_Text_of_the_U.S._Constitution"},
		{"USConst/impeachment", "sole power of impeachment", "Full_Text_of_the_U.S._Constitution"},
		// US Constitutional Amendments
		{"USAmend/bear_arms", "right to bear arms", "Full_Text_of_the_U.S._Constitutional_amendments"},
		{"USAmend/cruel_unusual", "cruel and unusual punishment", "Full_Text_of_the_U.S._Constitutional_amendments"},
		{"USAmend/double_jeopardy", "double jeopardy", "Full_Text_of_the_U.S._Constitutional_amendments"},
		// Canadian Constitution
		{"Canada/dominion", "dominion under the name of canada", "Canadian_Constitution_Act_1867"},
		{"Canada/governor_general", "governor general", "Canadian_Constitution_Act_1867"},
		{"Canada/maritime_provinces", "maritime provinces", "Canadian_Constitution_Act_1867"},
		// Mexico Constitution
		{"Mexico/ejido", "ejido", "Mexico_1917_rev_2015_Constitution_-_Constitute"},
		{"Mexico/amparo", "amparo", "Mexico_1917_rev_2015_Constitution_-_Constitute"},
		{"Mexico/federal_district", "federal district municipio", "Mexico_1917_rev_2015_Constitution_-_Constitute"},
	}

	// Short display names for the summary matrix.
	docShort := map[string]string{
		"Full_Text_of_the_U.S._Constitution":              "USConst",
		"Full_Text_of_the_U.S._Constitutional_amendments": "USAmend",
		"Canadian_Constitution_Act_1867":                  "Canada",
		"Mexico_1917_rev_2015_Constitution_-_Constitute":  "Mexico",
	}
	docOrder := []string{
		"Full_Text_of_the_U.S._Constitution",
		"Full_Text_of_the_U.S._Constitutional_amendments",
		"Canadian_Constitution_Act_1867",
		"Mexico_1917_rev_2015_Constitution_-_Constitute",
	}

	// Collect results for the summary matrix.
	type queryResult struct {
		phrase  string
		srcDoc  string
		docHits map[string]int
	}
	allResults := make([]queryResult, 0, len(queries))

	for _, q := range queries {
		q := q // capture
		t.Run(q.name, func(t *testing.T) {
			queryVec := tfidfEmbedding(q.phrase, embeddingDim, idf)
			results, err := idx.Search(queryVec, 10)
			if err != nil {
				t.Fatalf("Search(%q): %v", q.phrase, err)
			}

			docHits := make(map[string]int)
			for _, r := range results {
				docHits[docNameFromID(r.ID)]++
			}

			// Log per-query breakdown.
			t.Logf("Query: %q (expect: %s)", q.phrase, docShort[q.srcDoc])
			for _, doc := range docOrder {
				marker := ""
				if doc == q.srcDoc {
					marker = " <-- source"
				}
				t.Logf("  %-10s %d%s", docShort[doc], docHits[doc], marker)
			}

			if docHits[q.srcDoc] == 0 {
				t.Errorf("expected source doc %q in top-10 results for query %q", q.srcDoc, q.phrase)
			}

			allResults = append(allResults, queryResult{
				phrase:  q.phrase,
				srcDoc:  q.srcDoc,
				docHits: docHits,
			})
		})
	}

	// Log summary cross-document hit matrix.
	t.Log("")
	t.Log("Cross-document hit matrix (rows=queries, cols=documents):")
	header := fmt.Sprintf("  %-40s", "Query")
	for _, doc := range docOrder {
		header += fmt.Sprintf("  %8s", docShort[doc])
	}
	t.Log(header)
	t.Log(strings.Repeat("-", len(header)+4))

	for _, r := range allResults {
		row := fmt.Sprintf("  %-40s", r.phrase)
		for _, doc := range docOrder {
			cell := fmt.Sprintf("%d", r.docHits[doc])
			if doc == r.srcDoc {
				cell = fmt.Sprintf("[%d]", r.docHits[doc]) // bracket the source doc
			}
			row += fmt.Sprintf("  %8s", cell)
		}
		t.Log(row)
	}
}

// TestConstitutionDeleteAndSearch deletes ~20% of the corpus, then confirms the
// index still searches correctly over the survivors. There is no graph
// connectivity assertion: VectorIndex.Delete is a plain slice/map swap, not an
// HNSW graph mutation.
func TestConstitutionDeleteAndSearch(t *testing.T) {
	ids, vecs, _ := loadConstitutionCorpus(t)
	idx := buildCorpusIndex(t, ids, vecs)
	preLen := idx.Len()

	// Delete 20% of ids deterministically.
	rng := rand.New(rand.NewSource(99))
	deleteCount := preLen / 5
	perm := rng.Perm(len(ids))
	deleted := make(map[string]struct{}, deleteCount)
	for i := 0; i < deleteCount; i++ {
		id := ids[perm[i]]
		if !idx.Delete(id) {
			t.Fatalf("Delete(%s) returned false", id)
		}
		deleted[id] = struct{}{}
	}
	if idx.Len() != preLen-deleteCount {
		t.Fatalf("after delete Len = %d, want %d", idx.Len(), preLen-deleteCount)
	}

	// Build the surviving (ids, vecs) for GT. Delete reorders entries via
	// swap-with-last + truncate, so GT must be computed over survivors only.
	var survIDs []string
	var survVecs [][]float32
	for i, id := range ids {
		if _, gone := deleted[id]; gone {
			continue
		}
		survIDs = append(survIDs, id)
		survVecs = append(survVecs, vecs[i])
	}

	// Pick a surviving query and search.
	const k = 10
	query := survVecs[0]
	got, err := idx.Search(query, k)
	if err != nil {
		t.Fatalf("Search after delete: %v", err)
	}

	// Distance-bound exactness property over survivors (same eps rationale as Test 1).
	const eps = float32(1e-6)
	threshold := gtThreshold(query, survVecs, k)
	for i, r := range got {
		if r.Distance > threshold+eps {
			t.Errorf("survivor rank=%d: distance %.9g exceeds GT threshold %.9g (+eps)",
				i, r.Distance, threshold)
		}
	}

	// Deleted ids must never appear.
	for _, r := range got {
		if _, gone := deleted[r.ID]; gone {
			t.Errorf("deleted id %q appeared in results", r.ID)
		}
	}
	t.Logf("deleted %d/%d chunks; post-delete search returned %d results within GT bound",
		deleteCount, preLen, len(got))
}
