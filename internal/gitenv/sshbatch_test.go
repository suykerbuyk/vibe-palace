// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package gitenv

import (
	"slices"
	"strings"
	"testing"
)

// envValue is the value a subprocess sees: os/exec keeps the LAST value of a
// duplicated key, and the override is appended after the base environment.
func envValue(env []string, name string) (string, bool) {
	val, found := "", false
	for _, kv := range env {
		if n, v, _ := strings.Cut(kv, "="); n == name {
			val, found = v, true
		}
	}
	return val, found
}

// compose is what a caller does: the base environment, then the override.
func compose(env []string, core string) ([]string, []string) {
	o, args := SSHBatchModeOverride(env, core)
	return append(append([]string{}, env...), o...), args
}

// The operator's own command, with its key, must survive; BatchMode is added
// to it, never put in its place.
func TestSSHBatchModeOverride_AppendsToOperatorCommand(t *testing.T) {
	op := "ssh -i /home/u/.ssh/id_mdh -o IdentitiesOnly=yes -o IdentityAgent=none"
	env, args := compose([]string{"HOME=/h", "GIT_SSH_COMMAND=" + op}, "")
	got, ok := envValue(env, "GIT_SSH_COMMAND")
	if !ok || got != op+" -o BatchMode=yes" {
		t.Fatalf("GIT_SSH_COMMAND = %q, want the operator's command with BatchMode appended", got)
	}
	if len(args) != 0 {
		t.Fatalf("unexpected -c args %v", args)
	}
}

func TestSSHBatchModeOverride_CoreSSHCommand(t *testing.T) {
	env, args := compose([]string{"HOME=/h"}, "ssh -i /k")
	if _, ok := envValue(env, "GIT_SSH_COMMAND"); ok {
		t.Fatalf("GIT_SSH_COMMAND set although core.sshCommand is: %v", env)
	}
	want := []string{"-c", "core.sshCommand=ssh -i /k -o BatchMode=yes"}
	if !slices.Equal(args, want) {
		t.Fatalf("args = %v, want %v", args, want)
	}
}

func TestSSHBatchModeOverride_DefaultAndGitSSHAndIdempotent(t *testing.T) {
	env, _ := compose([]string{"HOME=/h"}, "")
	if got, _ := envValue(env, "GIT_SSH_COMMAND"); got != "ssh -o BatchMode=yes" {
		t.Fatalf("default = %q", got)
	}
	env, args := compose([]string{"GIT_SSH=/usr/bin/plink"}, "")
	if _, ok := envValue(env, "GIT_SSH_COMMAND"); ok || len(args) != 0 {
		t.Fatalf("GIT_SSH must be left alone: env=%v args=%v", env, args)
	}
	env, _ = compose([]string{"GIT_SSH_COMMAND=ssh -o BatchMode=yes -i k"}, "")
	if got, _ := envValue(env, "GIT_SSH_COMMAND"); got != "ssh -o BatchMode=yes -i k" {
		t.Fatalf("already-batch command changed: %q", got)
	}
}

// A path containing the word must not count as the option.
func TestSSHBatchModeOverride_PathContainingBatchModeIsNotTheOption(t *testing.T) {
	op := "/tmp/TestXBatchMode1/fake-ssh -i /k/BatchMode-key"
	env, _ := compose([]string{"GIT_SSH_COMMAND=" + op}, "")
	if got, _ := envValue(env, "GIT_SSH_COMMAND"); got != op+" -o BatchMode=yes" {
		t.Fatalf("got %q", got)
	}
	for _, already := range []string{"ssh -oBatchMode=yes", "ssh -o batchmode no", "ssh -o BatchMode=no -i k"} {
		env, _ := compose([]string{"GIT_SSH_COMMAND=" + already}, "")
		if got, _ := envValue(env, "GIT_SSH_COMMAND"); got != already {
			t.Fatalf("%q changed to %q", already, got)
		}
	}
}
