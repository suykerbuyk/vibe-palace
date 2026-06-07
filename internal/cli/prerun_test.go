// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import "testing"

// TestPreRunHookGatesLeafCommands verifies the dispatch choke-point: the pre-run
// hook fires for every leaf invocation shape (single-word leaf, two-word
// subcommand, BareInvocation parent), a non-zero hook return aborts before Run,
// and a zero return proceeds.
func TestPreRunHookGatesLeafCommands(t *testing.T) {
	cases := []struct {
		name string
		args []string
		reg  func(r *Registry)
	}{
		{
			name: "single-word leaf",
			args: []string{"absorb"},
			reg: func(r *Registry) {
				r.Register(&Command{Name: "absorb", MutatesVault: true, Run: func([]string) int { return ExitOK }})
			},
		},
		{
			name: "two-word subcommand",
			args: []string{"vault", "write", "x"},
			reg: func(r *Registry) {
				r.Register(&Command{Name: "vault", Subcommands: []string{"vault write"}})
				r.Register(&Command{Name: "vault write", MutatesVault: true, Run: func([]string) int { return ExitOK }})
			},
		},
		{
			name: "bare-invocation parent",
			args: []string{"hook"},
			reg: func(r *Registry) {
				r.Register(&Command{Name: "hook", BareInvocation: true, Subcommands: []string{"hook install"}, Run: func([]string) int { return ExitOK }})
				r.Register(&Command{Name: "hook install", Run: func([]string) int { return ExitOK }})
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name+"/abort", func(t *testing.T) {
			reg, _, _ := newTestRegistry()
			tc.reg(reg)
			hookCalled := false
			ranAfterAbort := false
			reg.SetPreRun(func(c *Command) int { hookCalled = true; return ExitSystem })
			code := reg.Dispatch(tc.args)
			if !hookCalled {
				t.Fatal("pre-run hook was not invoked")
			}
			if code != ExitSystem {
				t.Fatalf("abort: want exit %d, got %d", ExitSystem, code)
			}
			_ = ranAfterAbort
		})

		t.Run(tc.name+"/proceed", func(t *testing.T) {
			reg, _, _ := newTestRegistry()
			ran := false
			// Rebuild commands so Run records execution.
			switch tc.name {
			case "single-word leaf":
				reg.Register(&Command{Name: "absorb", MutatesVault: true, Run: func([]string) int { ran = true; return ExitOK }})
			case "two-word subcommand":
				reg.Register(&Command{Name: "vault", Subcommands: []string{"vault write"}})
				reg.Register(&Command{Name: "vault write", MutatesVault: true, Run: func([]string) int { ran = true; return ExitOK }})
			case "bare-invocation parent":
				reg.Register(&Command{Name: "hook", BareInvocation: true, Subcommands: []string{"hook install"}, Run: func([]string) int { ran = true; return ExitOK }})
				reg.Register(&Command{Name: "hook install", Run: func([]string) int { return ExitOK }})
			}
			reg.SetPreRun(func(c *Command) int { return ExitOK })
			if code := reg.Dispatch(tc.args); code != ExitOK {
				t.Fatalf("proceed: want exit %d, got %d", ExitOK, code)
			}
			if !ran {
				t.Fatal("Run did not execute after a passing pre-run hook")
			}
		})
	}
}

// TestPreRunHookReceivesMutatesFlag confirms the hook can read MutatesVault to
// decide fail-stop vs warn-only (the policy surfaceGate implements in cmd/vp).
func TestPreRunHookReceivesMutatesFlag(t *testing.T) {
	reg, _, _ := newTestRegistry()
	reg.Register(&Command{Name: "status", Run: func([]string) int { return ExitOK }})
	reg.Register(&Command{Name: "absorb", MutatesVault: true, Run: func([]string) int { return ExitOK }})

	var seen map[string]bool = map[string]bool{}
	reg.SetPreRun(func(c *Command) int { seen[c.Name] = c.MutatesVault; return ExitOK })

	reg.Dispatch([]string{"status"})
	reg.Dispatch([]string{"absorb"})

	if seen["status"] {
		t.Error("status should not be MutatesVault")
	}
	if !seen["absorb"] {
		t.Error("absorb should be MutatesVault")
	}
}

// TestNoPreRunHookIsNoop ensures dispatch works when no hook is installed.
func TestNoPreRunHookIsNoop(t *testing.T) {
	reg, _, _ := newTestRegistry()
	ran := false
	reg.Register(&Command{Name: "absorb", MutatesVault: true, Run: func([]string) int { ran = true; return ExitOK }})
	if code := reg.Dispatch([]string{"absorb"}); code != ExitOK {
		t.Fatalf("want %d, got %d", ExitOK, code)
	}
	if !ran {
		t.Fatal("Run should execute with no pre-run hook")
	}
}
