// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package surface

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// helperEnvVar marks the re-executed child of the arming tests below.
const helperEnvVar = "VP_AMBIENT_HELPER"

// TestAmbientRootIsSnapshotAtInit pins that the ambient snapshot is really
// taken, by the real init(), from the real config, in a process whose
// environment the test controls.
//
// 🔴 WHY A HELPER PROCESS. The snapshot is taken at package init, which has
// already happened by the time any test body runs — so a test cannot arrange
// the environment its own snapshot was taken in. An earlier draft compared the
// snapshot with the host's configured vault and asserted the empty case when
// the host had none; on such a host EVERY mutation (forcing "", inverting the
// gate, deleting init outright) produced exactly the expected empty value and
// the row could not fail. Re-executing this binary with a temporary
// XDG_CONFIG_HOME makes the expected value a known non-empty path on any POSIX
// host, so there is no empty branch to pass through. (Windows and plan9 skip —
// see writeAmbientConfig.)
func TestAmbientRootIsSnapshotAtInit(t *testing.T) {
	vault := filepath.Join(t.TempDir(), "v")
	cfgHome := t.TempDir()
	writeAmbientConfig(t, cfgHome, vault)

	out := runAmbientHelper(t, "TestAmbientHelperReportsSnapshot", []string{
		"XDG_CONFIG_HOME=" + cfgHome,
	})

	want := "AMBIENT=" + vault
	if !strings.Contains(out, want) {
		t.Fatalf("child reported %q, want a line %q\nfull output:\n%s", firstLine(out, "AMBIENT="), want, out)
	}
}

// TestAmbientHelperReportsSnapshot is the child half of the test above. It
// prints the init-time snapshot and nothing else.
func TestAmbientHelperReportsSnapshot(t *testing.T) {
	if os.Getenv(helperEnvVar) == "" {
		t.Skip("helper process only; driven by TestAmbientRootIsSnapshotAtInit")
	}
	fmt.Println("AMBIENT=" + AmbientVaultRootForTest())
}

// writeAmbientConfig lays down <cfgHome>/vibe-palace/config.toml naming vault.
//
// This one points XDG_CONFIG_HOME at cfgHome directly rather than deriving a
// path from HOME, so it is portable wherever os.UserConfigDir honours
// XDG_CONFIG_HOME — which is Unix and nothing else. os.UserConfigDir
// ($GOROOT/src/os/file.go) ignores XDG on darwin ($HOME/Library/Application
// Support), Windows (%AppData%) and plan9 ($home), so those are skipped rather
// than silently unreachable: on darwin the child's init would read the
// developer's REAL config and this row would assert against the wrong value.
// CI runs this package on Linux only; the skip keeps a macOS developer's
// `make test` honest instead of red.
func writeAmbientConfig(t *testing.T, cfgHome, vaultPath string) {
	t.Helper()
	switch runtime.GOOS {
	case "darwin", "ios", "windows", "plan9":
		t.Skipf("os.UserConfigDir does not honour XDG_CONFIG_HOME on %s, so this fixture cannot reach the child's config", runtime.GOOS)
	}
	dir := filepath.Join(cfgHome, "vibe-palace")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	body := fmt.Sprintf("vault_path = %q\n", vaultPath)
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// runAmbientHelper re-executes this test binary, running only the named test,
// and returns its combined output. It fails the test on a non-zero exit.
//
// 🔴 The child's environment is BUILT, never inherited-and-appended: an
// inherited XDG_CONFIG_HOME would survive and silently defeat a fixture that
// exists to exercise the os.UserConfigDir fallback. Only the variables listed
// here, plus those the caller passes, reach the child.
func runAmbientHelper(t *testing.T, testName string, env []string) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test binary: %v", err)
	}
	cmd := exec.Command(exe, "-test.run=^"+testName+"$", "-test.count=1")
	cmd.Env = append([]string{
		helperEnvVar + "=1",
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
	}, env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper %s failed: %v\noutput:\n%s", testName, err, out)
	}
	return string(out)
}

// firstLine returns the first line of out carrying prefix, for a focused
// failure message.
func firstLine(out, prefix string) string {
	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), prefix) {
			return strings.TrimSpace(line)
		}
	}
	return "<no " + prefix + " line>"
}
