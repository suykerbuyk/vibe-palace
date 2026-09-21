// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// vaultFlagSource is the vault_path source a root carries when --vault named it.
//
// It follows the vocabulary the resolvers already use — "cwd:<file>" and
// "global:<configpath>" (storage.ResolveVaultPath / ResolveGlobalVaultPath):
// a scope, then the thing an operator would look at to change the binding.
// For a flag that thing is the argument itself. Those two are inline literals
// at their only producer; this is a constant at its only producer, here.
const vaultFlagSource = "flag:--vault"

// vaultRootFlag is the --vault flag the vault git family (pull, push, sync,
// commit, tidy, status) shares. The migrate family keeps its own definitions:
// their help text describes a migration's scan, not a git operation.
var vaultRootFlag = cli.FlagDef{
	Name: "--vault",
	Arg:  "PATH",
	Help: "Act on the vault at PATH (the top level of its own git repository) instead of the configured vault_path",
}

// resolveVaultRootFlag returns the root a command will operate on and the
// source that bound it: the --vault value when given (tilde-expanded and made
// absolute, source vaultFlagSource), otherwise whatever fallback resolves.
//
// The fallback is the caller's, deliberately: the migrate family falls back to
// the cwd-aware project vault, the vault git family to the GLOBAL vault only.
// Unifying them would change one family's binding to match the other's.
func resolveVaultRootFlag(flagValue string, fallback func() (root, source string, err error)) (root, source string, err error) {
	if strings.TrimSpace(flagValue) != "" {
		abs, err := expandAndAbsPath(flagValue)
		if err != nil {
			return "", "", fmt.Errorf("--vault %s: %w", flagValue, err)
		}
		return abs, vaultFlagSource, nil
	}
	return fallback()
}

// resolveMigrationVaultRoot returns the vault root a migration will operate on:
// the --vault value when given (tilde-expanded and made absolute), otherwise the
// configured vault.
//
// --vault exists so the repair can be rehearsed against a THROWAWAY COPY of the
// archives before it is pointed at the real ones. That rehearsal is the only way
// to prove "heading-only diff" on real data, and a command that can only ever
// address the live vault cannot be rehearsed at all.
func resolveMigrationVaultRoot(flagValue string) (string, error) {
	root, _, err := resolveVaultRootFlag(flagValue, func() (string, string, error) {
		vault, err := openProjectVault()
		if err != nil {
			return "", "", fmt.Errorf("open vault: %w", err)
		}
		if vault.Root == "" {
			return "", "", fmt.Errorf("no vault configured; pass --vault PATH")
		}
		return vault.Root, "", nil
	})
	return root, err
}

// vaultRootFor resolves the root for a vault git command (pull, push, sync,
// commit, tidy, status), prints the binding to stderr, and runs the
// git_enabled preflight. It returns cli.ExitOK with the root, or the exit code
// the command should return — every failure here is the caller's to fix.
//
// Without --vault this is exactly the old vaultRoot() binding: the GLOBAL
// vault, never a cwd override. With --vault the root must pass
// refuseUnlessOwnGitRepo first, because git walks up from any directory it is
// handed: a --vault naming a subfolder of the live vault would have tidy commit
// into the live vault — the exact outcome the flag exists to prevent.
//
// mutating must match the command's mutates() registration (commit, tidy).
// surfaceGate runs EnforceFailStop in preRun, but against the CONFIGURED
// vault — it cannot see --vault. So when --vault names the root of a mutating
// command, the same fail-stop runs here against the named root, before any
// work: otherwise a copy written by a newer binary would be written by this
// older one, the exact write the gate exists to stop. Without --vault the
// configured root is the one preRun already gated, so it is not re-checked.
//
// git_enabled is still read from the HOST config whichever vault is named:
// it is host policy on whether vp runs git at all, not a property of a vault.
func vaultRootFor(cmdName, flagValue, verb string, mutating bool) (string, int) {
	root, source, err := resolveVaultRootFlag(flagValue, vaultRootWithSource)
	if err == nil && source == vaultFlagSource {
		err = refuseUnlessOwnGitRepo(root)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", cmdName, err)
		return "", cli.ExitUser
	}
	fmt.Fprintf(os.Stderr, "vault_path = %s\n", root)
	fmt.Fprintf(os.Stderr, "vault_path source = %s\n", source)
	if mutating && source == vaultFlagSource {
		if code := enforceSurfaceOnRoot(root); code != cli.ExitOK {
			return "", code
		}
	}
	if err := storage.RefuseIfGitDisabled(root, verb); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", cmdName, err)
		return "", cli.ExitUser
	}
	return root, cli.ExitOK
}

// enforceSurfaceOnRoot is surfaceGate's fail-stop for a root the caller
// resolved itself: it returns cli.ExitSystem, with the gate's error on stderr,
// when root carries a surface stamp newer than this binary (VP_SURFACE_GATE=warn
// downgrades that to a warning, as in preRun), and cli.ExitOK otherwise.
//
// It exists because preRun gates only the CONFIGURED vault. A mutating command
// that writes to any other root — one named by --vault, or one a migration
// resolves — must call this on that root before its first write. It assumes
// nothing about how root was resolved.
func enforceSurfaceOnRoot(root string) int {
	if err := surface.EnforceFailStop(root); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return cli.ExitSystem
	}
	return cli.ExitOK
}

// refuseUnlessOwnGitRepo admits a --vault root only when git sees it as the top
// level of its own work tree (storage.VaultGitOK), and names the state in every
// refusal. It is POSITIVE on purpose: storage.RefuseIfNestedVaultGit returns nil
// for every state except VaultGitNested, so it would admit a directory that is
// not under git at all, a broken repository, or one git cannot run against.
//
// The default refuses, so a VaultGit state added later fails closed here.
func refuseUnlessOwnGitRepo(root string) error {
	state, err := storage.InspectVaultGit(root)
	switch state {
	case storage.VaultGitOK:
		if err == nil {
			return nil
		}
		return fmt.Errorf("--vault %s: git cannot use this repository: %w", root, err)
	case storage.VaultNotGit:
		if _, serr := os.Stat(root); serr != nil {
			return fmt.Errorf("--vault %s: not a git repository: %w", root, serr)
		}
		return fmt.Errorf("--vault %s: not a git repository (no .git at or above it)", root)
	case storage.VaultGitNested:
		top, _ := storage.GitTopLevel(root)
		if top == "" {
			top = "an enclosing repository"
		}
		return fmt.Errorf("--vault %s: inside another repository (%s), not the top level of its own", root, top)
	case storage.VaultGitBroken:
		return fmt.Errorf("--vault %s: git cannot use this repository: %v", root, err)
	case storage.VaultGitUnavailable:
		return fmt.Errorf("--vault %s: a .git exists but git is not on PATH", root)
	default:
		return fmt.Errorf("--vault %s: unrecognised git state %d; refusing", root, state)
	}
}
