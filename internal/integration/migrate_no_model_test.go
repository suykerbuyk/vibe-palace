// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestIntegrationMigrateLoadsNoModel drives the real `vp` binary through every
// command whose inputs are now validated before the ONNX embedder is built,
// and proves none of them loads the model.
//
// The child gets an explicit environment — never os.Environ() — with HOME,
// the config dir and the cache dir under a fresh temp dir, and HTTPS_PROXY /
// HTTP_PROXY pointed at a dead loopback port. go-huggingface's downloader uses
// a default http.Client, so it honours the proxy: a regression cannot download
// anything, it fails closed. It is caught twice over:
//
//   - the exit code is wrong — construction against a fresh cache and a dead
//     proxy must fail, and that failure is exit 2;
//   - the HF hub cache directory appears. hub.DefaultCacheDir resolves to
//     ${XDG_CACHE_HOME:-$HOME/.cache}/huggingface/hub, and a failed attempt
//     still creates info/main.lock under it, so both locations are asserted
//     absent.
//
// <vault>/palace/.local/models is asserted only as a secondary: a download
// that fails never creates it, so on its own it would prove nothing.
//
// A regression costs about 20 s per subtest (hugot's retry loop) before it
// goes red. The test is not -short-gated: it needs no model, so it runs under
// `make test` as well as `make integration`.
func TestIntegrationMigrateLoadsNoModel(t *testing.T) {
	bin := buildVPBinary(t)

	cases := []struct {
		name string
		// args are formatted with the per-case root: {root} is replaced.
		args     []string
		cwd      string // relative to the case root; created if absent
		wantCode int
		wantOut  string // substring of combined output
	}{
		{
			name:     "mempalace missing export dry run",
			args:     []string{"migrate", "mempalace", "--export-path", "{root}/nope.json", "--dry-run"},
			wantCode: 1,
			wantOut:  "read export file",
		},
		{
			name:     "mempalace valid export dry run",
			args:     []string{"migrate", "mempalace", "--export-path", "{root}/export.json", "--dry-run"},
			wantCode: 0,
			wantOut:  "Would import: 0 projects, 0 sessions imported, 0 skipped, 1 drawers",
		},
		{
			name:     "vibevault missing source dry run",
			args:     []string{"migrate", "vibevault", "--vault-path", "{root}/nope", "--dry-run"},
			wantCode: 1,
			wantOut:  "has no Projects/",
		},
		{
			name:     "vibevault seeded source dry run",
			args:     []string{"migrate", "vibevault", "--dry-run"},
			wantCode: 0,
			wantOut:  "1 sessions imported",
		},
		{
			name:     "search bad slug",
			args:     []string{"search", "hello", "-p", "../Bad Slug"},
			wantCode: 1,
			wantOut:  "--project:",
		},
		{
			name:     "search unknown project",
			args:     []string{"search", "hello", "-p", "nosuchproject"},
			wantCode: 1,
			wantOut:  "no such project",
		},
		{
			name:     "search unknown detected project",
			args:     []string{"search", "hello"},
			cwd:      "cwd/unknownrepo",
			wantCode: 1,
			wantOut:  "detected from the current directory",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			home := filepath.Join(root, "home")
			cache := filepath.Join(root, "cache")
			cfg := filepath.Join(root, "cfg")
			vault := filepath.Join(root, "vault")
			cwd := filepath.Join(root, "cwd")
			if tc.cwd != "" {
				cwd = filepath.Join(root, tc.cwd)
			}
			seedNoModelEnv(t, root, home, cache, cfg, vault, cwd)

			args := make([]string, len(tc.args))
			for i, a := range tc.args {
				args[i] = strings.ReplaceAll(a, "{root}", root)
			}
			cmd := exec.Command(bin, args...)
			cmd.Dir = cwd
			cmd.Env = []string{
				"PATH=" + os.Getenv("PATH"),
				"HOME=" + home,
				"USERPROFILE=" + home,
				"XDG_CONFIG_HOME=" + cfg,
				"APPDATA=" + cfg,
				"XDG_CACHE_HOME=" + cache,
				"HTTPS_PROXY=http://127.0.0.1:1",
				"HTTP_PROXY=http://127.0.0.1:1",
				"NO_PROXY=",
			}
			out, err := cmd.CombinedOutput()
			code := 0
			if err != nil {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) {
					t.Fatalf("run vp %v: %v", args, err)
				}
				code = exitErr.ExitCode()
			}
			if code != tc.wantCode {
				t.Errorf("vp %v: exit %d, want %d\n%s", args, code, tc.wantCode, out)
			}
			if !strings.Contains(string(out), tc.wantOut) {
				t.Errorf("vp %v: output lacks %q\n%s", args, tc.wantOut, out)
			}
			for _, dir := range []string{
				filepath.Join(cache, "huggingface"),
				filepath.Join(home, ".cache", "huggingface"),
				filepath.Join(vault, "palace", ".local", "models"),
			} {
				if _, err := os.Stat(dir); !os.IsNotExist(err) {
					t.Errorf("vp %v created %s (stat err %v): the embedding model was constructed\n%s",
						args, dir, err, out)
				}
			}
		})
	}
}

// seedNoModelEnv lays out one case's sandbox: a config naming the vault, a
// vault holding one importable session, a one-drawer MemPalace export, and
// empty home, cache and cwd directories.
func seedNoModelEnv(t *testing.T, root, home, cache, cfg, vault, cwd string) {
	t.Helper()
	sessions := filepath.Join(vault, "Projects", "p", "sessions")
	for _, d := range []string{home, cache, filepath.Join(cfg, "vibe-palace"), sessions, cwd} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		filepath.Join(cfg, "vibe-palace", "config.toml"): `vault_path = "` + filepath.ToSlash(vault) + `"` + "\n",
		filepath.Join(sessions, "s1.md"): "---\nsession_id: \"s1\"\nproject: p\ndate: \"2026-09-01\"\n" +
			"title: \"One\"\nsummary: \"A session\"\ntag: implementation\n---\n## Transcript\n\nBuilt the worker pool.\n",
		filepath.Join(root, "export.json"): `{"exported_at":"2026-09-10T00:00:00Z","drawers":[` +
			`{"id":"d1","wing":"technical","room":"go","content":"Wrote a worker pool."}],"entities":[],"triples":[]}`,
	}
	for p, body := range files {
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
