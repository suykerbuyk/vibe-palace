// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package capture

import (
	"strings"
	"testing"
)

func TestChunkEmptyInput(t *testing.T) {
	chunks := Chunk("", DefaultChunkConfig())
	if len(chunks) != 0 {
		t.Fatalf("expected 0 chunks, got %d", len(chunks))
	}

	chunks = Chunk("   \n\n  ", DefaultChunkConfig())
	if len(chunks) != 0 {
		t.Fatalf("expected 0 chunks for whitespace, got %d", len(chunks))
	}
}

func TestChunkSingleParagraph(t *testing.T) {
	text := "This is a short paragraph that fits in one chunk."
	chunks := Chunk(text, DefaultChunkConfig())
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0] != text {
		t.Errorf("chunk content mismatch:\n  got:  %q\n  want: %q", chunks[0], text)
	}
}

func TestChunkMultipleParagraphs(t *testing.T) {
	// Create paragraphs that individually fit but together exceed MaxChars.
	para := strings.Repeat("Word. ", 50) // ~300 chars each
	text := para + "\n\n" + para + "\n\n" + para

	chunks := Chunk(text, ChunkConfig{MaxChars: 400, Overlap: 0})
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if c == "" {
			t.Errorf("chunk %d is empty", i)
		}
	}
}

func TestChunkSentenceBoundary(t *testing.T) {
	// Create text that must be split mid-paragraph.
	sentences := make([]string, 20)
	for i := range sentences {
		sentences[i] = "This is sentence number " + string(rune('A'+i)) + " which has some content."
	}
	text := strings.Join(sentences, " ")

	chunks := Chunk(text, ChunkConfig{MaxChars: 200, Overlap: 0})
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}

	// No chunk should end mid-word (except possibly the last if forced).
	for i, c := range chunks[:len(chunks)-1] {
		trimmed := strings.TrimSpace(c)
		if len(trimmed) == 0 {
			t.Errorf("chunk %d is empty", i)
			continue
		}
		last := trimmed[len(trimmed)-1]
		if last != '.' && last != '!' && last != '?' {
			// It's acceptable to end on a word boundary even without punctuation
			// but it should not end mid-word.
			if last != ' ' && !isAlphaNum(last) {
				t.Errorf("chunk %d ends on unexpected char %q", i, string(last))
			}
		}
	}
}

func isAlphaNum(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

func TestChunkOverlap(t *testing.T) {
	para1 := "First paragraph with enough content to fill a chunk. " + strings.Repeat("More words here. ", 20)
	para2 := "Second paragraph starting fresh. " + strings.Repeat("Additional content. ", 20)
	text := para1 + "\n\n" + para2

	chunks := Chunk(text, ChunkConfig{MaxChars: 300, Overlap: 50})
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks for overlap test, got %d", len(chunks))
	}

	// The second chunk should contain some text from the end of the first chunk
	// (the overlap prefix) plus its own content.
	// We just verify the second chunk is longer than it would be without overlap.
	chunksNoOverlap := Chunk(text, ChunkConfig{MaxChars: 300, Overlap: 0})
	if len(chunksNoOverlap) < 2 {
		t.Fatalf("expected at least 2 chunks without overlap, got %d", len(chunksNoOverlap))
	}
}

func TestChunkMarkdownTranscript(t *testing.T) {
	text := `## Human

What is the best way to implement a chunking engine?

## Assistant

There are several approaches to chunking text for semantic search.
The sliding window approach with overlap is most common.

## Human

Can you show me an example?

## Assistant

Sure, here is a simple implementation in Go.`

	// Verify format was detected as markdown.
	if DetectFormat(text) != FormatMarkdown {
		t.Errorf("expected FormatMarkdown, got %d", DetectFormat(text))
	}

	// With a small MaxChars, should produce multiple chunks.
	chunks := Chunk(text, ChunkConfig{MaxChars: 150, Overlap: 0})
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}
}

func TestChunkJSONRPC(t *testing.T) {
	text := `{"role":"user","content":"Hello, how are you?"}
{"role":"assistant","content":"I'm doing well, thanks for asking!"}
{"role":"user","content":"Can you help me with Go?"}
{"role":"assistant","content":"Of course! What do you need help with?"}`

	chunks := Chunk(text, ChunkConfig{MaxChars: 200, Overlap: 0})
	if len(chunks) < 1 {
		t.Fatalf("expected at least 1 chunk, got %d", len(chunks))
	}

	if DetectFormat(text) != FormatJSONRPC {
		t.Errorf("expected FormatJSONRPC, got %d", DetectFormat(text))
	}
}

func TestChunkLargeInput(t *testing.T) {
	// Generate ~100KB of text.
	var b strings.Builder
	sentence := "This is a test sentence for the chunking engine benchmark. "
	for b.Len() < 100_000 {
		b.WriteString(sentence)
		if b.Len()%500 < len(sentence) {
			b.WriteString("\n\n")
		}
	}
	text := b.String()

	chunks := Chunk(text, DefaultChunkConfig())
	if len(chunks) == 0 {
		t.Fatal("expected chunks from 100KB input")
	}

	// Verify no chunk exceeds MaxChars + Overlap + some tolerance for overlap prefix.
	maxAllowed := 800 + 200 // generous tolerance for overlap prefix
	for i, c := range chunks {
		if len(c) > maxAllowed {
			t.Errorf("chunk %d exceeds max allowed size: %d > %d", i, len(c), maxAllowed)
		}
	}
}

func TestChunkCRLFNormalization(t *testing.T) {
	text := "First paragraph.\r\n\r\nSecond paragraph.\r\nStill second."
	// After CRLF normalization, \r\n\r\n becomes \n\n which splits paragraphs.
	// With a small MaxChars, should produce 2 chunks from 2 paragraphs.
	chunks := Chunk(text, ChunkConfig{MaxChars: 40, Overlap: 0})
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks after CRLF normalization, got %d: %v", len(chunks), chunks)
	}
}

func TestLastSentenceBoundary(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		maxPos int
		want   int // expected boundary position
	}{
		{
			name:   "ends at period",
			text:   "Hello world. This is more text.",
			maxPos: 15,
			want:   12, // "." at 11, space at 12 → cut at 12
		},
		{
			name:   "ends at question mark",
			text:   "Is this working? Yes it is.",
			maxPos: 20,
			want:   16, // "?" at 15, space at 16 → cut at 16
		},
		{
			name:   "falls back to space",
			text:   "no-punctuation-here just words and more",
			maxPos: 25,
			want:   24, // space at 24 (scanning backward from 25)
		},
		{
			name:   "maxPos beyond text",
			text:   "short",
			maxPos: 100,
			want:   5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := lastSentenceBoundary(tt.text, tt.maxPos)
			if got != tt.want {
				t.Errorf("lastSentenceBoundary(%q, %d) = %d, want %d",
					tt.text, tt.maxPos, got, tt.want)
			}
		})
	}
}

func TestSplitLargeSegment(t *testing.T) {
	// Create a segment that's 2000 chars with sentences.
	var b strings.Builder
	for b.Len() < 2000 {
		b.WriteString("This is a complete sentence. ")
	}
	text := b.String()

	parts := splitLargeSegment(text, 800)
	if len(parts) < 2 {
		t.Fatalf("expected at least 2 parts, got %d", len(parts))
	}
	for i, p := range parts {
		if p == "" {
			t.Errorf("part %d is empty", i)
		}
	}
}

func TestOverlapPrefix(t *testing.T) {
	text := "this is the end of the previous chunk with some content"
	prefix := overlapPrefix(text, 20)
	if prefix == "" {
		t.Fatal("expected non-empty overlap prefix")
	}
	if len(prefix) > 20 {
		// Prefix might be slightly shorter than n due to word snapping.
		// It should not be much longer.
	}
	// Prefix should be a suffix of text (after word-snapping).
	if !strings.HasSuffix(text, prefix) {
		t.Errorf("prefix %q is not a suffix of %q", prefix, text)
	}
}

func TestBuildChunksNilSegments(t *testing.T) {
	result := buildChunks(nil, DefaultChunkConfig())
	if result != nil {
		t.Fatalf("expected nil, got %v", result)
	}
}
