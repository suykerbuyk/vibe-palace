// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/apperr"
)

// TestRefuseIfGitDisabled pins the refusal's shape: a caller-class error that
// wraps ErrGitDisabled, names the config path, and gives no remedy an agent
// could act on by editing the operator's config.
func TestRefuseIfGitDisabled(t *testing.T) {
	t.Run("enabled", func(t *testing.T) {
		hostConfig(t, str("git_enabled = true\n"))
		if err := RefuseIfGitDisabled("/v", "pull"); err != nil {
			t.Fatalf("RefuseIfGitDisabled = %v, want nil", err)
		}
	})
	t.Run("disabled", func(t *testing.T) {
		path := hostConfig(t, str("git_enabled = false\n"))
		err := RefuseIfGitDisabled("/v", "pull")
		if !errors.Is(err, ErrGitDisabled) {
			t.Fatalf("RefuseIfGitDisabled = %v, want ErrGitDisabled", err)
		}
		if !apperr.IsCaller(err) {
			t.Errorf("refusal is not caller-class, so it would turn health amber: %v", err)
		}
		msg := err.Error()
		for _, want := range []string{"refusing to pull", "git is disabled", path} {
			if !strings.Contains(msg, want) {
				t.Errorf("refusal %q lacks %q", msg, want)
			}
		}
		for _, forbidden := range []string{"set git_enabled", "git_enabled = true"} {
			if strings.Contains(msg, forbidden) {
				t.Errorf("refusal %q instructs a config change (%q)", msg, forbidden)
			}
		}
	})
	t.Run("unreadable", func(t *testing.T) {
		hostConfig(t, str("git_enabled = \"no\"\n"))
		err := RefuseIfGitDisabled("/v", "pull")
		if !errors.Is(err, ErrGitConfigUnreadable) || errors.Is(err, ErrGitDisabled) {
			t.Fatalf("RefuseIfGitDisabled = %v, want ErrGitConfigUnreadable and not ErrGitDisabled", err)
		}
		if !apperr.IsCaller(err) {
			t.Errorf("unreadable refusal is not caller-class: %v", err)
		}
	})
}
