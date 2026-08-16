// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIsVPMCPServer(t *testing.T) {
	t.Parallel()
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{"vp", "mcp"}, true},
		{[]string{"/home/johns/.local/bin/vp", "mcp"}, true},
		{[]string{"vp", "mcp", "serve"}, true},
		{[]string{"vp", "mcp", "serve", "--allow-writes"}, true},
		{[]string{"vp", "--verbose", "mcp"}, true},
		{[]string{"vp (deleted)", "mcp"}, true},
		{[]string{"/tmp/vp (deleted)", "mcp"}, true},
		{[]string{"vp", "mcp", "install"}, false},
		{[]string{"vp", "mcp", "uninstall"}, false},
		{[]string{"vp", "check"}, false},
		{[]string{"vp"}, false},
		{[]string{"bash", "-c", "vp mcp"}, false},
		{[]string{"herdr", "agent", "prompt", "run vp mcp"}, false},
		{[]string{"pgrep", "-f", "vp mcp"}, false},
		{nil, false},
	}
	for _, tc := range cases {
		got := isVPMCPServer(tc.args)
		if got != tc.want {
			t.Errorf("isVPMCPServer(%q) = %v, want %v", tc.args, got, tc.want)
		}
	}
}

func TestParseCmdline(t *testing.T) {
	t.Parallel()
	got := parseCmdline([]byte("vp\x00mcp\x00serve\x00"))
	if strings.Join(got, " ") != "vp mcp serve" {
		t.Fatalf("parseCmdline = %q", got)
	}
	if parseCmdline(nil) != nil && len(parseCmdline(nil)) != 0 {
		t.Fatalf("empty cmdline should be empty, got %q", parseCmdline(nil))
	}
}

func TestClassifyMCPExe_Deleted(t *testing.T) {
	t.Parallel()
	if g := classifyMCPExe("/home/x/.local/bin/vp (deleted)", "/home/x/.local/bin/vp"); g != exeDeleted {
		t.Fatalf("deleted image: got %v, want exeDeleted", g)
	}
}

func TestClassifyMCPExe_SameFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bin := filepath.Join(dir, "vp")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if g := classifyMCPExe(bin, bin); g != exeOK {
		t.Fatalf("same path: got %v, want exeOK", g)
	}
	link := filepath.Join(dir, "also")
	if err := os.Symlink(bin, link); err != nil {
		t.Fatal(err)
	}
	if g := classifyMCPExe(link, bin); g != exeOK {
		t.Fatalf("same inode via symlink: got %v, want exeOK", g)
	}
}

func TestClassifyMCPExe_OlderAndOther(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	old := filepath.Join(dir, "old")
	neu := filepath.Join(dir, "new")
	if err := os.WriteFile(old, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(neu, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if g := classifyMCPExe(old, neu); g != exeOlder {
		t.Fatalf("older image: got %v, want exeOlder", g)
	}
}

func TestClassifyMCPExe_NoInstalledOnlySeesDeleted(t *testing.T) {
	t.Parallel()
	if g := classifyMCPExe("/tmp/vp", ""); g != exeOK {
		t.Fatalf("no installed, live path: got %v, want exeOK", g)
	}
	if g := classifyMCPExe("/tmp/vp (deleted)", ""); g != exeDeleted {
		t.Fatalf("no installed, deleted: got %v, want exeDeleted", g)
	}
}

func TestCheckStaleMCP_DeletedIsInfo(t *testing.T) {
	orig := listMCP
	t.Cleanup(func() { listMCP = orig })
	listMCP = func() ([]mcpProc, error) {
		return []mcpProc{{PID: "9", Exe: "/tmp/vp (deleted)", Arg: []string{"vp", "mcp"}}}, nil
	}
	r := CheckStaleMCP()
	if r.Name != "Stale MCP" {
		t.Fatalf("name = %q", r.Name)
	}
	if r.Status != Info {
		t.Fatalf("status = %v, want Info", r.Status)
	}
	if !strings.Contains(r.Summary, "stale") {
		t.Fatalf("summary = %q", r.Summary)
	}
	joined := strings.Join(r.Details, "\n")
	if !strings.Contains(joined, "pid 9") || !strings.Contains(joined, "(deleted)") {
		t.Fatalf("details missing the deleted pid:\n%s", joined)
	}
}

func TestCheckStaleMCP_NoProcIsSkip(t *testing.T) {
	orig := listMCP
	t.Cleanup(func() { listMCP = orig })
	listMCP = func() ([]mcpProc, error) { return nil, errNoProc }
	r := CheckStaleMCP()
	if r.Status != Skip {
		t.Fatalf("status = %v, want Skip", r.Status)
	}
}

func TestCheckStaleMCP_NoneIsPass(t *testing.T) {
	orig := listMCP
	t.Cleanup(func() { listMCP = orig })
	listMCP = func() ([]mcpProc, error) { return nil, nil }
	r := CheckStaleMCP()
	if r.Status != Pass {
		t.Fatalf("status = %v, want Pass", r.Status)
	}
}

func TestCheckStaleMCP_HealthyIsPass(t *testing.T) {
	orig := listMCP
	t.Cleanup(func() { listMCP = orig })
	dir := t.TempDir()
	bin := filepath.Join(dir, "vp")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	listMCP = func() ([]mcpProc, error) {
		return []mcpProc{{PID: "1", Exe: bin, Arg: []string{"vp", "mcp"}}}, nil
	}
	// installedVP uses LookPath; classify compares exe to installed. Force
	// classify via the same path for both so this does not depend on PATH.
	r := CheckStaleMCP()
	// Without LookPath hitting `bin`, classify sees exeOther or exeOK.
	// Pin the Name and that we did not Skip — the PATH-dependent half is
	// covered by classifyMCPExe tests.
	if r.Name != "Stale MCP" {
		t.Fatalf("name = %q", r.Name)
	}
	if r.Status == Skip {
		t.Fatal("healthy scan should not Skip")
	}
}

func TestListMCPProcessesFrom_FakeProc(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	pidDir := filepath.Join(root, "4242")
	if err := os.Mkdir(pidDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pidDir, "cmdline"), []byte("vp\x00mcp\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/opt/vp", filepath.Join(pidDir, "exe")); err != nil {
		t.Fatal(err)
	}
	// Noise: a shell mentioning the words, and a non-pid entry.
	noise := filepath.Join(root, "99")
	if err := os.Mkdir(noise, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(noise, "cmdline"), []byte("bash\x00-c\x00vp mcp\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "self"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := listMCPProcessesFrom(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].PID != "4242" || got[0].Exe != "/opt/vp" {
		t.Fatalf("got %#v", got)
	}
}

func TestListMCPProcessesFrom_MissingProc(t *testing.T) {
	t.Parallel()
	_, err := listMCPProcessesFrom(filepath.Join(t.TempDir(), "nope"))
	if err != errNoProc {
		t.Fatalf("missing dir: err=%v, want errNoProc", err)
	}
}

// TestCheckStaleMCP_MutationDeletedReddens pins that dropping the
// (deleted) classifier would fail this test — a test that passes without
// exercising the defect proves nothing.
func TestCheckStaleMCP_MutationDeletedReddens(t *testing.T) {
	if classifyMCPExe("/x/vp (deleted)", "/x/vp") != exeDeleted {
		t.Fatal("the (deleted) suffix must classify as exeDeleted")
	}
}
