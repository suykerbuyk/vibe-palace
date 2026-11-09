// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package templates_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
)

// TestVaultfsWriteRefusesAmbientVault_NoStorageLink is the one row that can see
// whether the ambient snapshot is ARMED, as opposed to whether the seat fires.
//
// 🔴 WHY IT LIVES IN internal/templates, AND WHY NO OTHER ROW REPLACES IT.
//
// Eight packages reach a vault write seat without linking internal/storage:
// atomicfile, commands, context, enrichment, shims, templates, vaultfs,
// wrapstate. internal/commands -> templates.PreserveBackup -> vaultfs.Create is
// a real vault write with a caller-supplied root in one of them. An earlier
// design armed the snapshot from an init() in internal/storage, which never
// runs in those eight test binaries — so the guard would have been unarmed in
// exactly the binaries that can write a vault, while the old seat inside
// StampForPath (which needed no injected state) was being deleted. Every other
// row in this suite injects the root through SetAmbientVaultRoot, so every
// other row stays GREEN with the arming deleted. This one does not: it has no
// seam anywhere, and the root reaches the guard only through the real init()
// reading a real config file.
//
// It calls vaultfs.Create DIRECTLY rather than templates.PreserveBackup, even
// though PreserveBackup is the production chain that makes the hole concrete.
// PreserveBackup names its destination through BackupName, which always appends
// ".bak", and ResolveStampDir drops any ".bak" basename before the old guard
// ran — so a .bak path was never guarded pre-change and could not show a
// before/after difference. "Templates/commands/x.md" carries no .bak suffix and
// no excluded segment, so it is stamp-eligible; and RefuseUninitialisedAbs does
// not judge a path outside the project trees, so nothing upstream pre-empts it.
func TestVaultfsWriteRefusesAmbientVault_NoStorageLink(t *testing.T) {
	// 🔴 XDG_CONFIG_HOME ONLY STEERS os.UserConfigDir ON UNIX
	// ($GOROOT/src/os/file.go: darwin uses $HOME/Library/Application Support,
	// Windows %AppData%, plan9 $home). Elsewhere the child's init() would read
	// the DEVELOPER'S REAL config, so the ambient root would be the real vault
	// while this fixture writes a temp one — neither arm would fire and the row
	// would red unmutated, on a correct build. Skip rather than mislead. CI runs
	// this package on Linux only; macOS is a real developer host here.
	switch runtime.GOOS {
	case "darwin", "ios", "windows", "plan9":
		t.Skipf("os.UserConfigDir does not honour XDG_CONFIG_HOME on %s, so this fixture cannot reach the child's config", runtime.GOOS)
	}
	vault := filepath.Join(t.TempDir(), "v")
	if err := os.MkdirAll(vault, 0o755); err != nil {
		t.Fatalf("mkdir vault: %v", err)
	}
	cfgHome := t.TempDir()
	dir := filepath.Join(cfgHome, "vibe-palace")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	body := fmt.Sprintf("vault_path = %q\n", vault)
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test binary: %v", err)
	}
	cmd := exec.Command(exe, "-test.run=^TestVaultfsCreateHelper$", "-test.count=1")
	// Built, never inherited-and-appended.
	cmd.Env = []string{
		"VP_AMBIENT_HELPER=1",
		"VP_AMBIENT_HELPER_VAULT=" + vault,
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"XDG_CONFIG_HOME=" + cfgHome,
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper failed: %v\noutput:\n%s", err, out)
	}
	report := string(out)

	// A child that RETURNED AN ERROR must not satisfy this row: that is the
	// pre-emption shape that silently emptied two earlier rows in this unit.
	if idx := strings.Index(report, "NOPANIC="); idx >= 0 {
		t.Fatalf("child reported %s for a vaultfs.Create into the ambient vault root %s; expected the ambient-vault panic",
			strings.SplitN(report[idx:], "\n", 2)[0], vault)
	}
	idx := strings.Index(report, "PANIC=")
	if idx < 0 {
		t.Fatalf("child reported neither PANIC= nor NOPANIC=\noutput:\n%s", report)
	}
	line := strings.SplitN(report[idx:], "\n", 2)[0]
	if !strings.Contains(line, "CONFIGURED vault") {
		t.Errorf("panic came from the wrong arm: %s", line)
	}
	if !strings.Contains(line, vault) {
		t.Errorf("panic message does not name the configured root %s: %s", vault, line)
	}
}

// TestVaultfsCreateHelper is the child half. It performs the write and reports
// whether the guard panicked, on one line, so the parent can tell a refusal
// from a returned error.
func TestVaultfsCreateHelper(t *testing.T) {
	if os.Getenv("VP_AMBIENT_HELPER") == "" {
		t.Skip("helper process only; driven by TestVaultfsWriteRefusesAmbientVault_NoStorageLink")
	}
	vault := os.Getenv("VP_AMBIENT_HELPER_VAULT")
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("PANIC=%s\n", strings.ReplaceAll(fmt.Sprint(r), "\n", " | "))
		}
	}()
	_, err := vaultfs.Create(vault, "Templates/commands/x.md", "body\n")
	fmt.Printf("NOPANIC=%v\n", err)
}
