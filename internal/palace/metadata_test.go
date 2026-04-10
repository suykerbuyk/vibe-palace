// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package palace

import (
	"slices"
	"testing"
)

func TestDetectWing(t *testing.T) {
	tests := []struct {
		name       string
		project    string
		sourcePath string
		want       string
	}{
		{"project set", "my-project", "", "my-project"},
		{"project and sourcePath", "my-project", "/src/main.go", "my-project"},
		{"sourcePath only", "", "/workspace/src/main.go", "workspace"},
		{"sourcePath single component", "", "main.go", "main.go"},
		{"both empty", "", "", "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DetectWing(tt.project, tt.sourcePath); got != tt.want {
				t.Errorf("DetectWing(%q, %q) = %q, want %q", tt.project, tt.sourcePath, got, tt.want)
			}
		})
	}
}

func TestDetectRoom_ContentKeywords(t *testing.T) {
	tests := []struct {
		content string
		want    string
	}{
		{"We need to write a test with full coverage.", "testing"},
		{"Run the test suite and check assertion results.", "testing"},
		{"Deploy the Docker container to production.", "devops"},
		{"The API endpoint returns 404.", "api"},
		{"Run the SQL migration on the database.", "data"},
		{"Update the TOML config file.", "config"},
		{"There's a panic and a crash in the logs.", "debugging"},
		{"Refactor the old code to use new patterns.", "refactoring"},
		{"The architecture uses a clean abstraction layer.", "architecture"},
		{"Profiling shows high latency in the hot loop.", "performance"},
		{"Check the auth credentials for the service.", "security"},
		{"The sky is blue and water is wet.", "general"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := DetectRoom(tt.content, "", nil); got != tt.want {
				t.Errorf("DetectRoom(%q) = %q, want %q", tt.content, got, tt.want)
			}
		})
	}
}

func TestDetectRoom_CaseInsensitive(t *testing.T) {
	if got := DetectRoom("THE API ENDPOINT IS BROKEN", "", nil); got != "api" {
		t.Errorf("expected case-insensitive match: got %q, want %q", got, "api")
	}
}

func TestDetectRoom_BenchmarkGoesToPerformance(t *testing.T) {
	got := DetectRoom("running the benchmark suite", "", nil)
	if got != "performance" {
		t.Errorf("'benchmark' should map to performance, got %q", got)
	}
}

func TestDetectRoom_SourcePath(t *testing.T) {
	tests := []struct {
		name       string
		sourcePath string
		want       string
	}{
		// Go
		{"go test file", "internal/foo/bar_test.go", "testing"},
		// Python
		{"python test file", "tests/test_models.py", "testing"},
		{"python test suffix", "models_test.py", "testing"},
		// TypeScript/JavaScript
		{"ts test file", "src/utils.test.ts", "testing"},
		{"ts spec file", "src/utils.spec.ts", "testing"},
		{"js test file", "src/utils.test.js", "testing"},
		{"js spec file", "src/utils.spec.js", "testing"},
		{"tsx test file", "src/App.test.tsx", "testing"},
		// Java
		{"java test file", "src/FooTest.java", "testing"},
		// Rust
		{"rust test file", "src/parser_test.rs", "testing"},
		// Ruby
		{"ruby test file", "test/models_test.rb", "testing"},
		{"ruby spec file", "spec/models_spec.rb", "testing"},
		// DevOps — multi-platform CI
		{"dockerfile", "deploy/Dockerfile", "devops"},
		{"docker-compose", "docker-compose.yml", "devops"},
		{"makefile", "Makefile", "devops"},
		{"gitlab ci", ".gitlab-ci.yml", "devops"},
		{"jenkinsfile", "Jenkinsfile", "devops"},
		{"cmakelists", "CMakeLists.txt", "devops"},
		{"meson build", "meson.build", "devops"},
		{"autotools configure.ac", "configure.ac", "devops"},
		{"conanfile txt", "conanfile.txt", "config"},
		{"conanfile py", "conanfile.py", "config"},
		{"vcpkg json", "vcpkg.json", "config"},
		// Config — multi-language
		{"env file", ".env", "config"},
		{"config toml", "config.toml", "config"},
		{"config yaml", "config.yaml", "config"},
		{"editorconfig", ".editorconfig", "config"},
		{"tsconfig", "tsconfig.json", "config"},
		{"pyproject", "pyproject.toml", "config"},
		{"tox ini", "tox.ini", "config"},
		// Unknown
		{"unknown file", "main.go", "general"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Use content with no keyword hits so only filename matters.
			if got := DetectRoom("nothing interesting here", tt.sourcePath, nil); got != tt.want {
				t.Errorf("DetectRoom(_, %q, nil) = %q, want %q", tt.sourcePath, got, tt.want)
			}
		})
	}
}

func TestDetectRoom_CustomKeywords(t *testing.T) {
	custom := map[string][]string{
		"audio": {"wav", "mp3", "codec"},
	}
	got := DetectRoom("convert the wav file to mp3", "", custom)
	if got != "audio" {
		t.Errorf("custom keywords should match: got %q, want %q", got, "audio")
	}
}

func TestDetectRoom_CustomKeywordsCaseInsensitive(t *testing.T) {
	custom := map[string][]string{
		"audio": {"WAV", "MP3"},
	}
	got := DetectRoom("convert the wav file", "", custom)
	if got != "audio" {
		t.Errorf("custom keywords should be case-insensitive: got %q, want %q", got, "audio")
	}
}

func TestDetectRoom_CascadePriority(t *testing.T) {
	// Custom keywords (tier 1) should beat filename (tier 2) and content (tier 3).
	custom := map[string][]string{
		"audio": {"test"},
	}
	got := DetectRoom("test content", "foo_test.go", custom)
	if got != "audio" {
		t.Errorf("custom keywords should take priority: got %q, want %q", got, "audio")
	}

	// Filename (tier 2) should beat content (tier 3).
	got = DetectRoom("nothing here", "bar_test.go", nil)
	if got != "testing" {
		t.Errorf("filename should take priority over content: got %q, want %q", got, "testing")
	}
}

func TestDetectRoom_EmptyContent(t *testing.T) {
	if got := DetectRoom("", "", nil); got != "general" {
		t.Errorf("empty content should fallback to general: got %q", got)
	}
}

// TestDetectRoom_WordBoundary verifies that leading word-boundary matching
// prevents substring false positives while allowing legitimate matches.
func TestDetectRoom_WordBoundary(t *testing.T) {
	falsePositives := []struct {
		name    string
		content string
	}{
		{"contest not testing", "The contest was decided yesterday."},
		{"latest not testing", "The latest results are in."},
		{"prefix not debugging", "Use a prefix operator."},
		{"suffix not debugging", "Add the suffix to the string."},
		{"special not devops", "This is a special case."},
		{"decision not devops", "The decision was made quickly."},
		{"until not discoveries", "Wait until the build finishes."},
		{"utilization not discoveries", "CPU utilization is high."},
	}
	for _, tt := range falsePositives {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectRoom(tt.content, "", nil)
			if got != "general" {
				t.Errorf("false positive: %q classified as %q, want general", tt.content, got)
			}
		})
	}
}

// TestDetectRoom_StemMatching verifies that inflected word forms still match
// via leading word-boundary matching (no trailing \b).
func TestDetectRoom_StemMatching(t *testing.T) {
	stems := []struct {
		content string
		want    string
	}{
		{"We deployed the service to staging.", "devops"},
		{"All tests are passing with good coverage.", "testing"},
		{"The server crashed on startup.", "debugging"},
		{"She refactored the parser module.", "refactoring"},
		{"Mocking the service for test isolation.", "testing"},
		{"Multiple assertions failed.", "testing"},
	}
	for _, tt := range stems {
		t.Run(tt.want, func(t *testing.T) {
			if got := DetectRoom(tt.content, "", nil); got != tt.want {
				t.Errorf("stem match: %q = %q, want %q", tt.content, got, tt.want)
			}
		})
	}
}

// TestDetectRoom_CustomKeywordsDeterministic verifies that custom keyword
// map iteration is deterministic (sorted by room name).
func TestDetectRoom_CustomKeywordsDeterministic(t *testing.T) {
	custom := map[string][]string{
		"bravo": {"keyword"},
		"alpha": {"keyword"},
	}
	for i := range 100 {
		got := DetectRoom("this has the keyword here", "", custom)
		if got != "alpha" {
			t.Errorf("iteration %d: got %q, want %q (sorted determinism)", i, got, "alpha")
		}
	}
}

// TestDetectRoom_WeightedScoring verifies the weighted scoring behavior:
// low-weight keywords alone fall to "general", co-occurrence clears threshold,
// and high-weight keywords classify on a single hit.
func TestDetectRoom_WeightedScoring(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		// Single low-weight keyword → general (below threshold)
		{"lone test", "We need to write a test.", "general"},
		{"lone fix", "Please fix this issue.", "general"},
		{"lone error", "There was an error somewhere.", "general"},
		// Two low-weight hits in same room → clears threshold
		{"fix+error→debugging", "Need to fix this error in the output.", "debugging"},
		// High-weight keyword alone → classifies
		{"kubernetes alone", "Set up the kubernetes cluster.", "devops"},
		{"graphql alone", "Build the graphql schema.", "api"},
		{"segfault alone", "Got a segfault on line 42.", "debugging"},
		// Mixed rooms: dominant topic wins
		{"devops over debugging", "Used terraform to fix the deployment crash.", "devops"},
		{"api over testing", "Test the graphql endpoint with mock data.", "api"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DetectRoom(tt.content, "", nil); got != tt.want {
				t.Errorf("DetectRoom(%q) = %q, want %q", tt.content, got, tt.want)
			}
		})
	}
}

func TestDetectHall(t *testing.T) {
	tests := []struct {
		content string
		want    string
	}{
		{"We decided to use brute-force search.", HallDecisions},
		{"I chose the ONNX backend for embeddings.", HallDecisions},
		{"Discovered that the library has a bug.", HallDiscoveries},
		{"Turns out the cache was stale.", HallDiscoveries},
		{"I prefer tabs over spaces.", HallPreferences},
		{"You should use context.Background() here.", HallAdvice},
		{"Best practice is to validate inputs.", HallAdvice},
		{"The release happened last Tuesday.", HallEvents},
		{"We shipped the new feature.", HallEvents},
		{"The sky is blue and water is wet.", HallFacts},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := DetectHall(tt.content); got != tt.want {
				t.Errorf("DetectHall(%q) = %q, want %q", tt.content, got, tt.want)
			}
		})
	}
}

func TestDetectHallDefault(t *testing.T) {
	if got := DetectHall("just some ordinary text with no keywords"); got != HallFacts {
		t.Errorf("expected default hall %q, got %q", HallFacts, got)
	}
}

// TestDetectHall_WordBoundary verifies hall keyword word-boundary matching.
func TestDetectHall_WordBoundary(t *testing.T) {
	// "until" should not match discoveries via "til" substring
	got := DetectHall("Wait until the build finishes.")
	if got != HallFacts {
		t.Errorf("false positive: %q classified as %q, want %q", "until...", got, HallFacts)
	}
}

func TestDefaultRoomKeywords(t *testing.T) {
	m := DefaultRoomKeywords()
	if len(m) == 0 {
		t.Fatal("DefaultRoomKeywords() returned empty map")
	}
	expectedRooms := []string{"testing", "devops", "api", "data", "config",
		"debugging", "refactoring", "architecture", "performance", "security"}
	for _, room := range expectedRooms {
		if _, ok := m[room]; !ok {
			t.Errorf("missing expected room %q", room)
		}
	}
	// Verify "benchmark" is NOT in testing keywords.
	if slices.Contains(m["testing"], "benchmark") {
		t.Error("'benchmark' should not be in testing keywords")
	}
	// Verify "benchmark" IS in performance keywords.
	if !slices.Contains(m["performance"], "benchmark") {
		t.Error("'benchmark' should be in performance keywords")
	}
	// Verify no Go-specific keywords leaked into content keywords.
	// "_test.go" and "panic" should not appear as content keywords.
	if slices.Contains(m["testing"], "_test.go") {
		t.Error("'_test.go' is Go-specific and should not be a content keyword")
	}
	if slices.Contains(m["debugging"], "panic") {
		t.Error("'panic' is Go-specific and should not be a content keyword")
	}
	// Verify refined keywords are present.
	if !slices.Contains(m["architecture"], "system design") {
		t.Error("'system design' should be in architecture keywords")
	}
	if !slices.Contains(m["performance"], "profiling") {
		t.Error("'profiling' should be in performance keywords")
	}
}

func TestDefaultRoomKeywordsReturnsCopy(t *testing.T) {
	m1 := DefaultRoomKeywords()
	m1["testing"] = append(m1["testing"], "mutated")
	m2 := DefaultRoomKeywords()
	for _, kw := range m2["testing"] {
		if kw == "mutated" {
			t.Error("DefaultRoomKeywords should return a copy, not a reference")
		}
	}
}
