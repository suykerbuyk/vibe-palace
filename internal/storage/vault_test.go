// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}

	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"tilde slash", "~/documents", filepath.Join(home, "documents"), false},
		{"tilde alone", "~", home, false},
		{"absolute path", "/tmp/vault", "/tmp/vault", false},
		{"relative path", "relative/path", "relative/path", false},
		{"no tilde", "vault", "vault", false},
		{"tilde in middle", "path/~/weird", "path/~/weird", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := expandTilde(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("expandTilde(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("expandTilde(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestVaultRoot(t *testing.T) {
	t.Run("valid config", func(t *testing.T) {
		dir := t.TempDir()
		vaultDir := filepath.Join(dir, "myvault")
		configFile := filepath.Join(dir, "config.toml")
		if err := os.WriteFile(configFile, []byte(`vault_path = "`+vaultDir+`"`+"\n"), 0644); err != nil {
			t.Fatal(err)
		}

		got, err := VaultRoot(configFile)
		if err != nil {
			t.Fatalf("VaultRoot() error = %v", err)
		}
		if got != vaultDir {
			t.Errorf("VaultRoot() = %q, want %q", got, vaultDir)
		}
	})

	t.Run("missing file", func(t *testing.T) {
		_, err := VaultRoot("/nonexistent/config.toml")
		if err == nil {
			t.Error("VaultRoot with missing file should return error")
		}
	})

	t.Run("missing vault_path", func(t *testing.T) {
		dir := t.TempDir()
		configFile := filepath.Join(dir, "config.toml")
		if err := os.WriteFile(configFile, []byte("log_level = \"info\"\n"), 0644); err != nil {
			t.Fatal(err)
		}

		_, err := VaultRoot(configFile)
		if err == nil {
			t.Error("VaultRoot with missing vault_path should return error")
		}
	})

	t.Run("empty vault_path", func(t *testing.T) {
		dir := t.TempDir()
		configFile := filepath.Join(dir, "config.toml")
		if err := os.WriteFile(configFile, []byte("vault_path = \"\"\n"), 0644); err != nil {
			t.Fatal(err)
		}

		_, err := VaultRoot(configFile)
		if err == nil {
			t.Error("VaultRoot with empty vault_path should return error")
		}
	})

	t.Run("tilde in vault_path", func(t *testing.T) {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatal(err)
		}

		dir := t.TempDir()
		configFile := filepath.Join(dir, "config.toml")
		if err := os.WriteFile(configFile, []byte("vault_path = \"~/myvault\"\n"), 0644); err != nil {
			t.Fatal(err)
		}

		got, err := VaultRoot(configFile)
		if err != nil {
			t.Fatalf("VaultRoot() error = %v", err)
		}
		want := filepath.Join(home, "myvault")
		if got != want {
			t.Errorf("VaultRoot() = %q, want %q", got, want)
		}
	})
}

func TestVaultConfigFilePath(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	got, err := VaultConfigFilePath()
	if err != nil {
		t.Fatalf("VaultConfigFilePath() error = %v", err)
	}
	want := filepath.Join(tmp, "vibe-palace", "config.toml")
	if got != want {
		t.Errorf("VaultConfigFilePath() = %q, want %q", got, want)
	}
}

func TestNewVault(t *testing.T) {
	v := NewVault("/my/vault")
	if v.Root != "/my/vault" {
		t.Errorf("NewVault root = %q, want %q", v.Root, "/my/vault")
	}
}

func TestOpenVault(t *testing.T) {
	dir := t.TempDir()
	vaultDir := filepath.Join(dir, "vault")
	configFile := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(configFile, []byte(`vault_path = "`+vaultDir+`"`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	v, err := OpenVault(configFile)
	if err != nil {
		t.Fatalf("OpenVault() error = %v", err)
	}
	if v.Root != vaultDir {
		t.Errorf("OpenVault().Root = %q, want %q", v.Root, vaultDir)
	}
}
