// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

// Shared helpers for the e2e_*_test.go tier tests (init, githook, walkthrough,
// workflows), which port test/e2e/**'s bash harness onto testinfra.RunCLI.
//
// Filesystem/content assertions below are deliberately simple substring/stat
// checks — a faithful port of test/e2e/lib.sh's assert_* helpers, which used
// literal (or effectively literal) grep patterns throughout the retired
// scripts. No regexp engine is needed for any of them.
//
// The three retired e2e-internal Go helper binaries
// (test/e2e/internal/seeddrawer, test/e2e/internal/tomleq,
// test/e2e/workflows/mockllm) are inlined here as direct calls / an
// httptest.Server rather than re-ported as exec'd binaries — see
// seedDrawer, tomlStructEqual, and newMockLLMServer below.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/suykerbuyk/vibe-palace/internal/palace"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// gitInit runs a real `git init -q` in dir, failing the test on error. Every
// tier's cases need a real git repository (not a fake .git marker) since
// project.DetectSignal and the git-post-commit-hook installer both shell out
// to `git` themselves.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init -q in %s: %v\n%s", dir, err, out)
	}
}

// requireFileExists fails the test unless path exists and is a regular file
// (not a directory). Port of lib.sh's assert_file_exists.
func requireFileExists(t *testing.T, path string) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("expected file to exist: %s (%v)", path, err)
	}
	if fi.IsDir() {
		t.Fatalf("expected file, got directory: %s", path)
	}
}

// requireDirExists fails the test unless path exists and is a directory.
// Port of lib.sh's assert_dir_exists.
func requireDirExists(t *testing.T, path string) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("expected directory to exist: %s (%v)", path, err)
	}
	if !fi.IsDir() {
		t.Fatalf("expected directory, got file: %s", path)
	}
}

// requireAbsent fails the test if path exists at all (file or directory).
// Port of lib.sh's assert_file_absent / assert_dir_absent (both were the
// same os.Stat-based check under the hood).
func requireAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		t.Fatalf("expected absent, but exists: %s", path)
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat %s: %v", path, err)
	}
}

// requireContains fails the test unless substr appears in content. Port of
// lib.sh's assert_grep, applied to in-memory content (e.g. CLIResult.Stdout)
// rather than a log file on disk.
func requireContains(t *testing.T, content, substr string) {
	t.Helper()
	if !strings.Contains(content, substr) {
		t.Fatalf("expected %q in content, got:\n%s", substr, content)
	}
}

// requireNotContains is the negation of requireContains. Port of lib.sh's
// assert_not_grep.
func requireNotContains(t *testing.T, content, substr string) {
	t.Helper()
	if strings.Contains(content, substr) {
		t.Fatalf("expected %q ABSENT from content, got:\n%s", substr, content)
	}
}

// requireFileContains reads path and applies requireContains to its bytes.
func requireFileContains(t *testing.T, path, substr string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	requireContains(t, string(data), substr)
}

// requireFileNotContains reads path and applies requireNotContains to its
// bytes.
func requireFileNotContains(t *testing.T, path, substr string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	requireNotContains(t, string(data), substr)
}

// projectConfigPath mirrors test/e2e/lib.sh's vault_project_config_path, but
// goes through the real storage.Vault.ProjectConfigFile accessor instead of
// re-deriving the "{vault}/Projects/{project}/config.toml" layout by hand —
// a Go test can just call the production code that owns that path.
// hostProjectConfigPath is the HOST-LOCAL per-project config file, which is
// where `tune rooms --apply` and `discover rooms --apply` write after R2 of
// task move-per-project-config-out-of-the-shared-vault.
//
// 🔴 NOT projectConfigPath. That one resolves the VAULT's
// Projects/<slug>/config.toml, which no production code writes any more
// (`grep -rn '\.WriteScoringConfig(' --include=*.go . | grep -v _test.go` is
// empty). An apply assertion pointed there compares an untouched file to
// itself and passes no matter what the writer does.
//
// It resolves through the process environment, which testinfra.IsolateEnv has
// already pointed at the same XDG_CONFIG_HOME the CLI subprocess gets, so this
// is the very file the subprocess wrote.
func hostProjectConfigPath(t *testing.T, project string) string {
	t.Helper()
	p, err := storage.HostProjectConfigPath(project)
	if err != nil {
		t.Fatalf("HostProjectConfigPath(%q): %v", project, err)
	}
	return p
}

func projectConfigPath(t *testing.T, vaultRoot, project string) string {
	t.Helper()
	p, err := storage.NewVault(vaultRoot).ProjectConfigFile(project)
	if err != nil {
		t.Fatalf("ProjectConfigFile(%q): %v", project, err)
	}
	return p
}

// seedDrawer inlines test/e2e/internal/seeddrawer/main.go's entire body as a
// direct storage call: one storage.Vault.AppendDrawer against the vault
// rooted at homeDir/vibe-palace-vault (vp's default vault path, matching
// what the retired binary itself assumed via os.UserHomeDir()). No exec, no
// binary build — this is the "real simplification available only because
// we're inside a Go test process" the migration plan calls out.
func seedDrawer(t *testing.T, homeDir, project, room, content string) {
	t.Helper()
	vault := storage.NewVault(filepath.Join(homeDir, "vibe-palace-vault"))
	wing := palace.DetectWing(project, "")
	d := storage.Drawer{
		Hall:       "",
		Content:    content,
		SourceType: "seed",
		AddedBy:    "e2e-rig",
		FiledAt:    time.Now().UTC().Format(time.RFC3339),
	}
	if err := vault.AppendDrawer(project, wing, room, d); err != nil {
		t.Fatalf("seedDrawer(%q, %q): %v", project, room, err)
	}
}

// tomlStructEqual inlines test/e2e/internal/tomleq/main.go: decode both TOML
// files into map[string]any and compare by reflect.DeepEqual. sha256 is not
// safe here — toml.NewEncoder is not byte-stable across round-trips — so
// idempotency assertions need structural, not textual, equality.
func tomlStructEqual(t *testing.T, pathA, pathB string) bool {
	t.Helper()
	var a, b map[string]any
	if _, err := toml.DecodeFile(pathA, &a); err != nil {
		t.Fatalf("decode %s: %v", pathA, err)
	}
	if _, err := toml.DecodeFile(pathB, &b); err != nil {
		t.Fatalf("decode %s: %v", pathB, err)
	}
	return reflect.DeepEqual(a, b)
}

// mockLLMServer is an in-process replacement for
// test/e2e/workflows/mockllm/main.go: an httptest.Server speaking the subset
// of the OpenAI-compatible /chat/completions API that `vp tune` needs. The
// retired binary read $MOCK_RESPONSE_FILE per request (not once at startup);
// this closes over a Go string instead, which the plan calls out as a
// strictly better replacement (no env var indirection, no subprocess, no
// polling-for-port-file dance — httptest.NewServer already solves the
// ephemeral-port problem the retired binary hand-rolled).
type mockLLMServer struct {
	*httptest.Server
	content string
	// responder, when set, builds the assistant message from the request body
	// instead of replaying content. See reflectAllTo.
	responder func(reqBody string) string
}

// setResponse changes the JSON content of the next /chat/completions
// response. Safe to call between requests (single-threaded test use).
func (m *mockLLMServer) setResponse(content string) { m.content = content }

// newMockLLMServer starts a mock LLM server with an initial response body.
// The caller must not close it; t.Cleanup already arranges that.
// drawerIDInPrompt matches the `drawer_id: <id>` lines BuildClassificationPrompt
// writes into the user message.
var drawerIDInPrompt = regexp.MustCompile(`(?m)^drawer_id: (.+)$`)

// reflectAllTo returns a responder that classifies EVERY drawer the prompt
// names into room, echoing back the real drawer_ids.
//
// 🔴 A STATIC RESPONSE CANNOT DRIVE THIS PATH. Judgments are matched to samples
// by drawer_id, and the ids are generated at seed time, so a canned list of
// placeholder ids is silently skipped by ParseJudgments and every run comes
// back with zero judgments and zero proposals — which means `--apply` writes
// nothing and any assertion downstream of it proves nothing.
//
// It decodes the request rather than regexing the raw body: the prompt arrives
// as a JSON string, so its newlines are two-character escapes and a line-
// anchored match against the wire bytes silently finds nothing.
func reflectAllTo(room string) func(string) string {
	return func(reqBody string) string {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal([]byte(reqBody), &req); err != nil {
			return "[]"
		}
		var out []string
		for _, msg := range req.Messages {
			for _, m := range drawerIDInPrompt.FindAllStringSubmatch(msg.Content, -1) {
				id := strings.TrimSpace(m[1])
				if id == "" {
					continue
				}
				out = append(out, fmt.Sprintf(
					`{"drawer_id": %q, "room": %q, "confidence": 0.9, "reasoning": "reflected by the mock"}`, id, room))
			}
		}
		return "[" + strings.Join(out, ",\n") + "]"
	}
}

func newMockLLMServer(t *testing.T, initialContent string) *mockLLMServer {
	t.Helper()
	m := &mockLLMServer{content: initialContent}
	mux := http.NewServeMux()
	mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		content := m.content
		if m.responder != nil {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			content = m.responder(string(body))
		}
		resp := map[string]any{
			"choices": []any{
				map[string]any{
					"message": map[string]any{
						"role":    "assistant",
						"content": content,
					},
				},
			},
			"usage": map[string]any{
				"prompt_tokens":     10,
				"completion_tokens": 10,
				"total_tokens":      20,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	m.Server = httptest.NewServer(mux)
	t.Cleanup(m.Server.Close)
	return m
}

// writeProjectLLMConfig appends a [palace.llm] stanza to the project's vault
// config.toml, port of test/e2e/lib.sh's write_project_llm_config. The
// internal/llm client treats Endpoint as a BASE URL and appends
// "/chat/completions" itself, so the base URL is written as-is. Returns the
// env var assignment the caller must add to its RunCLI env
// ("VP_MOCK_KEY=mock-key") for api_key_env resolution.
func writeProjectLLMConfig(t *testing.T, vaultRoot, project, url string) string {
	t.Helper()
	cfg := projectConfigPath(t, vaultRoot, project)
	f, err := os.OpenFile(cfg, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open %s for append: %v", cfg, err)
	}
	defer f.Close()
	body := "\n[palace.llm]\n" +
		"endpoint = \"" + url + "\"\n" +
		"model = \"mock-model\"\n" +
		"api_key_env = \"VP_MOCK_KEY\"\n" +
		"max_tokens = 256\n"
	if _, err := f.WriteString(body); err != nil {
		t.Fatalf("append llm config to %s: %v", cfg, err)
	}
	return "VP_MOCK_KEY=mock-key"
}

// sha256File is the byte-level counterpart of tomlStructEqual: it sees a change
// a structural compare normalises away, and it treats an absent file as a
// stable empty rather than failing, so it can bracket a file that may or may
// not be created by the run under test.
func sha256File(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "<absent>"
		}
		t.Fatalf("sha256File %s: %v", path, err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
