// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/projectdir"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// TestAppendUnderLockRefusesAmbientVault pins the append seat, the second of
// the two write funnels. It has two sub-tests that use DIFFERENT INSTRUMENTS on
// purpose, because they pin different things and one instrument cannot do both.
//
//   - seat_fires drives the public entry point, AppendIterationOwned, and pins
//     that the seat fires on the real production path. For that the project
//     must be INITIALISED, because AppendIterationOwned calls EnsureVaultDir
//     before it takes the lock and EnsureVaultDir refuses an uninitialised
//     project first.
//
//   - uninitialised_project pins WHERE IN THE REFUSAL BAND the seat sits, and
//     it CANNOT use the public entry point for exactly the reason above: with
//     an uninitialised project, EnsureVaultDir returns the same error whether
//     the guard is first or last, so the public entry point is green either way
//     and the ordering stays unpinned. It therefore calls the production
//     primitive directly, holding the lock the primitive's contract requires
//     (internal/storage/project_gate_test.go already calls appendUnderLock
//     directly; this one also acquires, because the guard-first arm panics and
//     a deferred release must unwind it).
func TestAppendUnderLockRefusesAmbientVault(t *testing.T) {
	t.Run("seat_fires", func(t *testing.T) {
		root := t.TempDir()
		testutil.InitProject(t, root, "p") // scaffold markers: past EnsureVaultDir
		restore := surface.SetAmbientVaultRoot(root)
		defer restore()
		v := NewVault(root)

		var n, derived int
		var err error
		func() {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("AppendIterationOwned returned (n=%d, derived=%d, err=%v) for an append into the ambient vault root %s; expected a panic",
						n, derived, err, root)
				}
				msg, _ := r.(string)
				if !strings.Contains(msg, "CONFIGURED vault") {
					t.Fatalf("panic came from the wrong arm: %v", r)
				}
				if !strings.Contains(msg, root) {
					t.Errorf("panic message does not name the configured root %s: %v", root, r)
				}
			}()
			n, derived, err = v.AppendIterationOwned("p", "t", "b", nil)
		}()

		// The pre-emption check: if this fires, the sub-test stopped reaching
		// the seat and its mutation is inert.
		if errors.Is(err, projectdir.ErrUninitialisedProject) {
			t.Fatalf("pre-empted by projectdir instead of reaching the seat: %v", err)
		}
	})

	t.Run("uninitialised_project", func(t *testing.T) {
		root := t.TempDir() // deliberately NO scaffold marker
		restore := surface.SetAmbientVaultRoot(root)
		defer restore()
		v := NewVault(root)
		abs := filepath.Join(root, "Projects", "p", "iterations.md")

		release, err := vaultlock.Acquire(v.Root, abs)
		if err != nil {
			t.Fatalf("acquire: %v", err)
		}
		defer release()

		// With the guard seated FIRST the ambient refusal wins. Seated below
		// projectdir it would never run and the caller would see a quiet,
		// ignorable typed error instead — the incident shape this guard exists
		// to replace, since the 2026-11-03 writer scaffolded a project nobody
		// had initialised.
		var appendErr error
		func() {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("appendUnderLock returned %v for an append into the ambient vault root %s; expected the ambient-vault panic (the guard has slipped below projectdir)",
						appendErr, root)
				}
				msg, _ := r.(string)
				if !strings.Contains(msg, "CONFIGURED vault") {
					t.Fatalf("panic came from the wrong arm: %v", r)
				}
			}()
			appendErr = v.appendUnderLock(abs, []byte("line\n"))
		}()

		// Non-vacuity: prove the projectdir refusal really would have fired
		// here, so "the guard won" is a statement about ordering and not about
		// an absent competitor. With the ambient root unset, the same call must
		// return ErrUninitialisedProject.
		unset := surface.SetAmbientVaultRoot("")
		defer unset()
		if err := v.appendUnderLock(abs, []byte("line\n")); !errors.Is(err, projectdir.ErrUninitialisedProject) {
			t.Fatalf("fixture invariant broken: with no ambient root the append returned %v, want ErrUninitialisedProject — the ordering assertion above proves nothing", err)
		}
	})
}

// TestInlinedAmbientRootMatchesResolveGlobalVaultPath pins that surface's
// inlined ambient-root read has not drifted from storage.VaultRoot, which is
// the original it was copied from because the import cycle forbids calling it.
//
// 🔴 IT IS A HELPER PROCESS WITH A TEMPORARY HOME, AND EVERY PART OF THAT IS
// LOAD-BEARING. The snapshot is taken at package init, which has already
// happened by the time any test body runs, so a test cannot arrange the
// environment its own snapshot was taken in. Re-executing this binary with a
// controlled HOME and no XDG_CONFIG_HOME makes the expected value a known path
// on any POSIX host (see hostConfigDirUnder: Windows and plan9 skip). HOME
// drives both os.UserConfigDir and os.UserHomeDir (which the tilde expansion
// calls).
//
// 🔴 TWO SUB-TESTS, AND THEY MUST NOT BE COLLAPSED BACK INTO ONE. There are
// three points where the copy can silently diverge from storage.VaultRoot — the
// config-path derivation (os.UserConfigDir vs os.Getenv), the tilde expansion,
// and the trailing-separator cleaning (filepath.Abs) — and NO SINGLE
// vault_path reaches all three:
//
//   - expandTilde ends in filepath.Join, and filepath.Join ALREADY CLEANS. So
//     under a "~/vault/" fixture the trailing separator is gone before
//     filepath.Abs is reached, Abs is a no-op, and deleting it changes nothing.
//     Measured: "~/vault/" -> Join -> "/home/x/vault" -> Abs -> "/home/x/vault".
//   - An ABSOLUTE vault_path skips the Join entirely, so the trailing separator
//     survives expansion and Abs is the only thing that removes it.
//     Measured: "/abs/vault/" -> (unchanged) -> Abs -> "/abs/vault".
//
// So the tilde case pins expandTilde and the absolute case pins the cleaning;
// each is blind to the other's statement. An earlier revision of this test used
// the tilde fixture alone and claimed to cover all three modes — the Abs arm was
// unreachable and the mutation that deletes Abs ran GREEN. Both cases pin the
// config-path derivation, which is why that mode needs no third fixture.
func TestInlinedAmbientRootMatchesResolveGlobalVaultPath(t *testing.T) {
	// Pins expandTilde. Abs is a no-op here, by construction — see above.
	t.Run("tilde", func(t *testing.T) {
		home := t.TempDir()
		writeHomeConfig(t, home, "~/vault/")
		got, want := runParityHelper(t, home)
		assertParity(t, got, want, filepath.Join(home, "vault"))
	})

	// Pins expandAmbientTilde's BARE "~" arm, which is a separate branch from
	// "~/" and which M10-a cannot reach: M10-a deletes the whole call, so it
	// reds on the "~/" case and says nothing about this one. filepath.Join(home,
	// "") is home, so the arm is real: a vault_path of exactly "~" names HOME.
	t.Run("bare_tilde", func(t *testing.T) {
		home := t.TempDir()
		writeHomeConfig(t, home, "~")
		got, want := runParityHelper(t, home)
		assertParity(t, got, want, home)
	})

	// Pins the trailing-separator cleaning (filepath.Abs). The vault_path is
	// absolute AND carries a trailing separator, so expansion leaves it
	// untouched and only Abs removes the separator. This is also the shape of a
	// real operator config.
	t.Run("absolute_with_trailing_separator", func(t *testing.T) {
		home := t.TempDir()
		vault := filepath.Join(t.TempDir(), "vault")
		writeHomeConfig(t, home, vault+string(filepath.Separator))
		got, want := runParityHelper(t, home)
		// The expected value carries NO trailing separator: that is the whole
		// assertion. Guard the fixture too, so a later edit that drops the
		// separator from the config body cannot quietly make this a duplicate
		// of the tilde case.
		if want := fmt.Sprintf("vault%c\"\n", filepath.Separator); !strings.HasSuffix(readHomeConfig(t, home), want) {
			t.Fatalf("fixture invariant broken: vault_path must end in a separator (want suffix %q), or Abs has nothing to clean", want)
		}
		assertParity(t, got, want, vault)
	})
}

// hostConfigDirUnder returns the directory os.UserConfigDir resolves to for a
// process whose HOME is home, on the current GOOS.
//
// 🔴 THE FIXTURE PATH IS NOT PORTABLE AND MUST NOT BE HARDCODED.
// os.UserConfigDir ($GOROOT/src/os/file.go) reads $XDG_CONFIG_HOME then
// $HOME/.config on Unix, $HOME/Library/Application Support on darwin/ios,
// %AppData% on Windows and $home on plan9. Setting HOME alone is sufficient on
// the POSIX targets — UserConfigDir and os.UserHomeDir (which the tilde
// expansion calls) both read $HOME there — so these fixtures set HOME and
// nothing else, and they SKIP where that is not enough rather than pretend to
// cover it. CI runs this package on Linux only; macOS is a real developer host
// for this project, which is why darwin is handled rather than skipped.
func hostConfigDirUnder(t *testing.T, home string) string {
	t.Helper()
	switch runtime.GOOS {
	case "windows", "plan9":
		t.Skipf("fixture sets HOME only; os.UserConfigDir reads %s on %s, which these helpers do not plumb",
			map[string]string{"windows": "%AppData%", "plan9": "$home"}[runtime.GOOS], runtime.GOOS)
		return ""
	case "darwin", "ios":
		return filepath.Join(home, "Library", "Application Support")
	default:
		return filepath.Join(home, ".config")
	}
}

// writeHomeConfig lays down the host config under home naming vaultPath
// verbatim — the trailing separator, or its absence, IS the fixture, so this
// writes the string it is given and never normalises it.
func writeHomeConfig(t *testing.T, home, vaultPath string) {
	t.Helper()
	dir := filepath.Join(hostConfigDirUnder(t, home), "vibe-palace")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	body := fmt.Sprintf("vault_path = %q\n", vaultPath)
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// readHomeConfig reads back what writeHomeConfig wrote, for a fixture-invariant
// assertion.
func readHomeConfig(t *testing.T, home string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(hostConfigDirUnder(t, home), "vibe-palace", "config.toml"))
	if err != nil {
		t.Fatalf("read back config: %v", err)
	}
	return string(b)
}

// runParityHelper re-executes this test binary against the given HOME and
// returns the child's two readings of the ambient root.
//
// 🔴 The child's environment is BUILT, never inherited-and-appended: an
// inherited XDG_CONFIG_HOME would survive and defeat the os.UserConfigDir
// fallback both fixtures exercise. XDG_CONFIG_HOME is deliberately absent.
func runParityHelper(t *testing.T, home string) (got, want string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test binary: %v", err)
	}
	cmd := exec.Command(exe, "-test.run=^TestInlinedAmbientRootHelper$", "-test.count=1")
	cmd.Env = []string{
		"VP_AMBIENT_HELPER=1",
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper failed: %v\noutput:\n%s", err, out)
	}
	got, want, childErr := parseParityReport(t, string(out))
	if childErr != "" {
		t.Fatalf("child's ResolveGlobalVaultPath errored: %s\noutput:\n%s", childErr, out)
	}
	return got, want
}

// assertParity checks the resolver against the LITERAL expected value as well
// as the inlined copy against the resolver. The literal arm matters: a mutation
// that breaks BOTH readers in the same way would otherwise pass.
func assertParity(t *testing.T, got, want, expect string) {
	t.Helper()
	if want != expect {
		t.Errorf("resolver WANT=%q, expected %q", want, expect)
	}
	if got != want {
		t.Errorf("inlined snapshot GOT=%q != resolver WANT=%q", got, want)
	}
}

// TestInlinedAmbientRootHelper is the child half of the parity test.
func TestInlinedAmbientRootHelper(t *testing.T) {
	if os.Getenv("VP_AMBIENT_HELPER") == "" {
		t.Skip("helper process only; driven by TestInlinedAmbientRootMatchesResolveGlobalVaultPath")
	}
	// 🔴 WITHOUT THIS THE TWO READERS RUN IN DIFFERENT ENVIRONMENTS. The
	// snapshot was taken at init, against the launched environment. This body
	// runs after TestMain, and TestMain is RunHermetic, which points
	// XDG_CONFIG_HOME at the checked-in fixture — whose config.toml carries no
	// vault_path — so ResolveGlobalVaultPath would resolve from the fixture and
	// error, failing this row with the feature present. UseAmbientConfig
	// restores the launched value; it was unset, and an unset ambient is
	// restored as empty, which os.UserConfigDir treats the same way. It is
	// t.Setenv-scoped, so no other test in the package sees it.
	testutil.UseAmbientConfig(t)

	got := surface.AmbientVaultRootForTest()
	want, _, err := ResolveGlobalVaultPath()
	errStr := ""
	if err != nil {
		errStr = err.Error()
	}
	fmt.Printf("PARITY GOT=%s WANT=%s ERR=%s\n", got, want, errStr)
}

// parseParityReport pulls the child's one report line apart.
func parseParityReport(t *testing.T, out string) (got, want, errStr string) {
	t.Helper()
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "PARITY ") {
			continue
		}
		rest := strings.TrimPrefix(line, "PARITY ")
		gotIdx := strings.Index(rest, "GOT=")
		wantIdx := strings.Index(rest, " WANT=")
		errIdx := strings.Index(rest, " ERR=")
		if gotIdx != 0 || wantIdx < 0 || errIdx < wantIdx {
			t.Fatalf("malformed parity line %q", line)
		}
		return rest[len("GOT="):wantIdx], rest[wantIdx+len(" WANT=") : errIdx], rest[errIdx+len(" ERR="):]
	}
	t.Fatalf("no PARITY line in helper output:\n%s", out)
	return "", "", ""
}

// TestInlinedAmbientRootEmptyVaultPathIsNotTheCwd pins resolveAmbientRoot's
// `cfg.VaultPath == ""` clause, which no other fixture reaches.
//
// 🔴 WHY IT NEEDS ITS OWN ROW. Delete that clause and the empty string falls
// through: expandAmbientTilde("") returns "" (not a ~ path), and filepath.Abs("")
// is the PROCESS CWD — so on a host whose config carries `vault_path = ""` the
// cwd would be stored as the ambient vault root, and the guard would start
// refusing writes to whatever directory the process happened to start in. No
// other fixture writes an empty vault_path, so without this row the clause is
// unpinned and a later deletion is silent.
//
// It asserts BOTH that the snapshot is empty AND that it is not the child's cwd,
// so the mutation cannot pass by coincidence (an empty cwd, a cwd that happens
// to compare equal). It does not use runParityHelper: with no vault_path,
// storage.ResolveGlobalVaultPath errors by design, and that helper treats a
// child-side resolver error as a failure.
func TestInlinedAmbientRootEmptyVaultPathIsNotTheCwd(t *testing.T) {
	home := t.TempDir()
	writeHomeConfig(t, home, "")

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test binary: %v", err)
	}
	cmd := exec.Command(exe, "-test.run=^TestAmbientRootEmptyVaultPathHelper$", "-test.count=1")
	cmd.Env = []string{
		"VP_AMBIENT_HELPER=1",
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper failed: %v\noutput:\n%s", err, out)
	}

	got, cwd := parseEmptyCaseReport(t, string(out))
	if cwd == "" {
		t.Fatal("fixture invariant broken: the child reported no cwd, so the not-the-cwd assertion is vacuous")
	}
	if got != "" {
		t.Errorf("snapshot GOT=%q for an empty vault_path, want %q", got, "")
	}
	if got == cwd {
		t.Errorf("snapshot GOT=%q is the child's CWD — filepath.Abs(\"\") leaked through; the cfg.VaultPath == \"\" clause is gone", got)
	}
}

// TestAmbientRootEmptyVaultPathHelper is the child half: it reports the snapshot
// and its own working directory, so the parent can assert the snapshot is not
// the latter.
func TestAmbientRootEmptyVaultPathHelper(t *testing.T) {
	if os.Getenv("VP_AMBIENT_HELPER") == "" {
		t.Skip("helper process only; driven by TestInlinedAmbientRootEmptyVaultPathIsNotTheCwd")
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	fmt.Printf("EMPTYCASE GOT=%s CWD=%s\n", surface.AmbientVaultRootForTest(), cwd)
}

// parseEmptyCaseReport pulls the empty-case child's one report line apart.
func parseEmptyCaseReport(t *testing.T, out string) (got, cwd string) {
	t.Helper()
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "EMPTYCASE ") {
			continue
		}
		rest := strings.TrimPrefix(line, "EMPTYCASE ")
		cwdIdx := strings.Index(rest, " CWD=")
		if !strings.HasPrefix(rest, "GOT=") || cwdIdx < 0 {
			t.Fatalf("malformed empty-case line %q", line)
		}
		return rest[len("GOT="):cwdIdx], rest[cwdIdx+len(" CWD="):]
	}
	t.Fatalf("no EMPTYCASE line in helper output:\n%s", out)
	return "", ""
}
