// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package palace

import (
	"slices"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
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

// --- RoomClassifier tests ---

func TestRoomClassifier_DefaultMatchesDetectRoom(t *testing.T) {
	rc := NewRoomClassifier(nil, 0)
	tests := []string{
		"We need to write a test with full coverage.",
		"Deploy the Docker container to production.",
		"The API endpoint returns 404.",
		"Run the SQL migration on the database.",
		"The sky is blue and water is wet.",
		"Set up the kubernetes cluster.",
		"Got a segfault on line 42.",
	}
	for _, content := range tests {
		want := DetectRoom(content, "", nil)
		got := rc.Classify(content, "", nil)
		if got != want {
			t.Errorf("Classify(%q) = %q, want %q (DetectRoom result)", content, got, want)
		}
	}
}

func TestRoomClassifier_OverrideExistingRoom(t *testing.T) {
	overrides := map[string]WeightedOverride{
		"testing": {
			High: []string{"integration test", "e2e test"},
		},
	}
	rc := NewRoomClassifier(overrides, 0)

	// "integration test" is a high-weight override → should classify as testing.
	got := rc.Classify("run the integration test suite", "", nil)
	if got != "testing" {
		t.Errorf("override keyword should classify: got %q, want %q", got, "testing")
	}
}

func TestRoomClassifier_AddNewRoom(t *testing.T) {
	overrides := map[string]WeightedOverride{
		"ml": {
			High:   []string{"neural network", "transformer"},
			Medium: []string{"training"},
			Low:    []string{"epoch"},
		},
	}
	rc := NewRoomClassifier(overrides, 0)

	got := rc.Classify("train the neural network model", "", nil)
	if got != "ml" {
		t.Errorf("new room should classify: got %q, want %q", got, "ml")
	}

	// Single low-weight keyword should not classify (below threshold).
	got = rc.Classify("we completed one epoch", "", nil)
	if got != "general" {
		t.Errorf("lone low-weight in new room should be general: got %q, want %q", got, "general")
	}

	// Two medium keywords should classify.
	got = rc.Classify("transformer training pipeline", "", nil)
	if got != "ml" {
		t.Errorf("high+medium should classify: got %q, want %q", got, "ml")
	}
}

func TestRoomClassifier_CustomMinScore(t *testing.T) {
	// Lower threshold so a single low-weight keyword (0.3) classifies.
	rc := NewRoomClassifier(nil, 0.3)

	// With default threshold (0.6), lone "test" → general.
	// With threshold 0.3, lone "test" → testing.
	got := rc.Classify("We need to write a test.", "", nil)
	if got != "testing" {
		t.Errorf("lowered min_score should classify lone low keyword: got %q, want %q", got, "testing")
	}
}

func TestRoomClassifier_OverrideDoesNotMutateDefaults(t *testing.T) {
	overrides := map[string]WeightedOverride{
		"testing": {High: []string{"canary-keyword"}},
	}
	_ = NewRoomClassifier(overrides, 0)

	// The default classifier should NOT have the override keyword.
	got := DetectRoom("canary-keyword is here", "", nil)
	if got != "general" {
		t.Errorf("override leaked into defaults: got %q, want general", got)
	}
}

func TestRoomClassifier_FilenameStillWorks(t *testing.T) {
	rc := NewRoomClassifier(nil, 0)
	got := rc.Classify("nothing interesting", "foo_test.go", nil)
	if got != "testing" {
		t.Errorf("filename match should still work: got %q, want %q", got, "testing")
	}
}

func TestRoomClassifier_CustomKeywordsPriority(t *testing.T) {
	rc := NewRoomClassifier(nil, 0)
	custom := map[string][]string{
		"audio": {"test"},
	}
	// Custom keywords (tier 1) should beat filename (tier 2) and scoring (tier 3).
	got := rc.Classify("test content", "foo_test.go", custom)
	if got != "audio" {
		t.Errorf("custom keywords should take priority: got %q, want %q", got, "audio")
	}
}

// --- ClassifyWithScores tests ---

func TestClassifyWithScores_ContentTier(t *testing.T) {
	rc := NewRoomClassifier(nil, 0)
	res := rc.ClassifyWithScores("Set up the kubernetes cluster for deployment.", "", nil)
	if res.Room != "devops" {
		t.Errorf("Room = %q, want devops", res.Room)
	}
	if res.Tier != "content" {
		t.Errorf("Tier = %q, want content", res.Tier)
	}
	if res.Score <= 0 {
		t.Errorf("Score = %f, want > 0", res.Score)
	}
	if len(res.Scores) == 0 {
		t.Error("Scores map should be populated")
	}
	// devops score should be the highest.
	for room, score := range res.Scores {
		if score > res.Score && room != res.Room {
			t.Errorf("room %q has higher score %f than winner %q (%f)", room, score, res.Room, res.Score)
		}
	}
}

func TestClassifyWithScores_FilenameTier(t *testing.T) {
	rc := NewRoomClassifier(nil, 0)
	res := rc.ClassifyWithScores("nothing interesting here", "foo_test.go", nil)
	if res.Room != "testing" {
		t.Errorf("Room = %q, want testing", res.Room)
	}
	if res.Tier != "filename" {
		t.Errorf("Tier = %q, want filename", res.Tier)
	}
	if res.Borderline {
		t.Error("Borderline should be false for filename tier")
	}
	// Scores should still be populated even though filename won.
	if len(res.Scores) == 0 {
		t.Error("Scores map should be populated even for filename tier")
	}
}

func TestClassifyWithScores_CustomKeywordTier(t *testing.T) {
	rc := NewRoomClassifier(nil, 0)
	custom := map[string][]string{"audio": {"wav", "codec"}}
	res := rc.ClassifyWithScores("convert the wav file", "", custom)
	if res.Room != "audio" {
		t.Errorf("Room = %q, want audio", res.Room)
	}
	if res.Tier != "custom-keyword" {
		t.Errorf("Tier = %q, want custom-keyword", res.Tier)
	}
	if res.Borderline {
		t.Error("Borderline should be false for custom-keyword tier")
	}
	if len(res.Scores) == 0 {
		t.Error("Scores map should be populated even for custom-keyword tier")
	}
}

func TestClassifyWithScores_Fallback(t *testing.T) {
	rc := NewRoomClassifier(nil, 0)
	res := rc.ClassifyWithScores("The sky is blue and water is wet.", "", nil)
	if res.Room != "general" {
		t.Errorf("Room = %q, want general", res.Room)
	}
	if res.Tier != "fallback" {
		t.Errorf("Tier = %q, want fallback", res.Tier)
	}
	if res.Score != 0 {
		t.Errorf("Score = %f, want 0 for fallback", res.Score)
	}
	if res.Borderline {
		t.Error("Borderline should be false for fallback tier")
	}
}

func TestClassifyWithScores_Borderline(t *testing.T) {
	// Use default threshold 0.6. A single medium keyword (0.6) is exactly at
	// the threshold, which is within 0.2 of minScore → borderline.
	rc := NewRoomClassifier(nil, 0)
	// "api" alone is a medium keyword (0.6) — exactly at threshold.
	res := rc.ClassifyWithScores("check the api for errors", "", nil)
	if res.Tier != "content" {
		t.Fatalf("Tier = %q, want content", res.Tier)
	}
	if res.Score >= res.MinScore+0.2 {
		t.Fatalf("Score %f is too high to be borderline (threshold %f)", res.Score, res.MinScore)
	}
	if !res.Borderline {
		t.Errorf("Score %f with threshold %f should be borderline", res.Score, res.MinScore)
	}
}

func TestClassifyWithScores_NotBorderlineForFilename(t *testing.T) {
	rc := NewRoomClassifier(nil, 0)
	res := rc.ClassifyWithScores("nothing", "Dockerfile", nil)
	if res.Tier != "filename" {
		t.Fatalf("Tier = %q, want filename", res.Tier)
	}
	if res.Borderline {
		t.Error("Borderline must be false for non-content tiers")
	}
}

func TestClassifyWithScores_MatchesClassify(t *testing.T) {
	rc := NewRoomClassifier(nil, 0)
	inputs := []struct {
		content    string
		sourcePath string
	}{
		{"We need to write a test with full coverage.", ""},
		{"Deploy the Docker container to production.", ""},
		{"The API endpoint returns 404.", ""},
		{"Run the SQL migration on the database.", ""},
		{"The sky is blue and water is wet.", ""},
		{"Set up the kubernetes cluster.", ""},
		{"Got a segfault on line 42.", ""},
		{"nothing interesting", "foo_test.go"},
		{"nothing interesting", "Dockerfile"},
		{"nothing interesting", "config.toml"},
	}
	for _, tt := range inputs {
		want := rc.Classify(tt.content, tt.sourcePath, nil)
		got := rc.ClassifyWithScores(tt.content, tt.sourcePath, nil)
		if got.Room != want {
			t.Errorf("ClassifyWithScores(%q, %q).Room = %q, Classify = %q",
				tt.content, tt.sourcePath, got.Room, want)
		}
	}
}

func TestKeywordCoverage(t *testing.T) {
	rc := NewRoomClassifier(nil, 0)
	// Content that should fire kubernetes (devops, high) and deploy (devops, medium).
	reports := rc.KeywordCoverage("deploy the kubernetes cluster")
	var devops RoomKeywordReport
	for _, r := range reports {
		if r.Room == "devops" {
			devops = r
			break
		}
	}
	if len(devops.Fired) == 0 {
		t.Fatal("expected fired keywords for devops")
	}
	// kubernetes and deploy should be fired.
	firedSet := make(map[string]bool)
	for _, kw := range devops.Fired {
		firedSet[kw] = true
	}
	if !firedSet["kubernetes"] {
		t.Error("expected 'kubernetes' in fired")
	}
	if !firedSet["deploy"] {
		t.Error("expected 'deploy' in fired")
	}
	// Dead keywords should exist (e.g., terraform, ansible).
	if len(devops.Dead) == 0 {
		t.Error("expected dead keywords for devops")
	}
}

func TestRoomDefinitions(t *testing.T) {
	rc := NewRoomClassifier(nil, 0)
	defs := rc.RoomDefinitions()

	if len(defs) == 0 {
		t.Fatal("expected room definitions")
	}

	// Check that testing room exists with keywords.
	found := false
	for _, d := range defs {
		if d.Name == "testing" {
			found = true
			if len(d.Keywords) == 0 {
				t.Error("testing room should have keywords")
			}
			break
		}
	}
	if !found {
		t.Error("expected 'testing' room in definitions")
	}
}

func TestRoomDefinitions_WithOverrides(t *testing.T) {
	overrides := map[string]WeightedOverride{
		"ml": {High: []string{"neural network"}, Medium: []string{"training"}},
	}
	rc := NewRoomClassifier(overrides, 0)
	defs := rc.RoomDefinitions()

	found := false
	for _, d := range defs {
		if d.Name == "ml" {
			found = true
			if !slices.Contains(d.Keywords, "neural network") {
				t.Error("expected 'neural network' in ml keywords")
			}
			break
		}
	}
	if !found {
		t.Error("expected 'ml' room from overrides")
	}
}

func TestMatchingKeywords(t *testing.T) {
	rc := NewRoomClassifier(nil, 0)

	// Content with testing keywords ("test", "assert", "mock" are in the testing room).
	matches := rc.MatchingKeywords("running the test with assert and mock", "testing")
	if len(matches) == 0 {
		t.Fatal("expected matching keywords for testing room")
	}

	foundAssert := false
	for _, m := range matches {
		if m.Raw == "assert" {
			foundAssert = true
			if m.Weight != 0.6 {
				t.Errorf("assert weight = %f, want 0.6", m.Weight)
			}
		}
	}
	if !foundAssert {
		t.Error("expected 'assert' to match")
	}
}

func TestMatchingKeywords_NoMatch(t *testing.T) {
	rc := NewRoomClassifier(nil, 0)

	matches := rc.MatchingKeywords("nothing relevant here", "testing")
	if len(matches) != 0 {
		t.Errorf("expected no matches, got %d", len(matches))
	}
}

func TestMatchingKeywords_UnknownRoom(t *testing.T) {
	rc := NewRoomClassifier(nil, 0)

	matches := rc.MatchingKeywords("test content", "nonexistent")
	if matches != nil {
		t.Errorf("expected nil for unknown room, got %v", matches)
	}
}

func TestBuildClassifierFromConfig(t *testing.T) {
	cfg := storage.Config{
		PalaceScoringOverrides: map[string]storage.ScoringRoomOverride{
			"ml": {High: []string{"neural network"}, Medium: []string{"training"}},
		},
		PalaceMinScore: 0.4,
	}
	rc := BuildClassifierFromConfig(cfg)
	// Verify the override room classifies correctly.
	res := rc.ClassifyWithScores("neural network training deep learning", "", nil)
	if res.Room != "ml" {
		t.Errorf("room = %q, want ml", res.Room)
	}
}

func TestBuildClassifierFromConfig_Empty(t *testing.T) {
	rc := BuildClassifierFromConfig(storage.Config{})
	// Should use defaults — testing room should exist.
	defs := rc.RoomDefinitions()
	found := false
	for _, d := range defs {
		if d.Name == "testing" {
			found = true
		}
	}
	if !found {
		t.Error("expected default rooms with empty config")
	}
}
