// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// CheckStaleMCP reports running `vp mcp` (stdio) and `vp mcp serve` processes
// whose executable image has been deleted (`make install` replaced the inode)
// or is not the `vp` currently on PATH.
//
// The only input the producer registry hands a check is the vault root, which
// this does not need — it inspects this machine's process table, the same
// escape hatch host-surfaces uses for $HOME. When the check is invoked via
// MCP it runs in the server process, so it sees the server's host — which is
// the machine whose image matters.
//
// Advisory: Info, never Fail. Restarting an AI host is a human move.
func CheckStaleMCP() Result {
	r := Result{Name: "Stale MCP"}

	procs, err := listMCP()
	if err != nil {
		if errors.Is(err, errNoProc) {
			r.Status = Skip
			r.Summary = "no /proc"
			return r
		}
		r.Status = Info
		r.Summary = fmt.Sprintf("scan: %v", err)
		return r
	}
	if len(procs) == 0 {
		r.Status = Pass
		r.Summary = "no vp mcp processes"
		return r
	}

	installed := installedVP()
	var bad []string
	for _, p := range procs {
		switch classifyMCPExe(p.Exe, installed) {
		case exeDeleted:
			bad = append(bad, fmt.Sprintf("  pid %s: %s — make install replaced the inode; restart this AI host", p.PID, p.Exe))
		case exeOlder:
			bad = append(bad, fmt.Sprintf("  pid %s: %s is older than installed %s", p.PID, p.Exe, installed))
		case exeOther:
			bad = append(bad, fmt.Sprintf("  pid %s: %s is not the installed %s", p.PID, p.Exe, installed))
		}
	}
	if len(bad) == 0 {
		r.Status = Pass
		r.Summary = fmt.Sprintf("%d vp mcp process(es) match installed vp", len(procs))
		return r
	}
	r.Status = Info
	r.Summary = fmt.Sprintf("%d of %d vp mcp process(es) are stale", len(bad), len(procs))
	r.Details = append(bad,
		"A stale binary is a stale *server process*. `make install` is not enough:",
		"a long-lived `vp mcp` keeps executing the deleted inode. Restart the AI host;",
		"killing the server alone leaves that session with no vp_* tools.")
	return r
}

var (
	errNoProc = errors.New("check: no /proc")
	// listMCP is the process-table scan. Tests replace it.
	listMCP = listMCPProcesses
)

type mcpProc struct {
	PID string
	Exe string
	Arg []string
}

type exeClass int

const (
	exeOK exeClass = iota
	exeDeleted
	exeOlder
	exeOther
)

const deletedSuffix = " (deleted)"

func installedVP() string {
	p, err := exec.LookPath("vp")
	if err != nil {
		return ""
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	return p
}

// classifyMCPExe decides whether a running image is the installed vp.
// A missing installed path can only detect the (deleted) form — there is
// nothing to compare mtimes or inodes against.
func classifyMCPExe(exe, installed string) exeClass {
	if strings.HasSuffix(exe, deletedSuffix) {
		return exeDeleted
	}
	if installed == "" || exe == "" {
		return exeOK
	}
	a, ea := os.Stat(exe)
	b, eb := os.Stat(installed)
	if ea == nil && eb == nil {
		if os.SameFile(a, b) {
			return exeOK
		}
		if a.ModTime().Before(b.ModTime()) {
			return exeOlder
		}
		return exeOther
	}
	if filepath.Clean(exe) != filepath.Clean(installed) {
		return exeOther
	}
	return exeOK
}

func listMCPProcesses() ([]mcpProc, error) {
	return listMCPProcessesFrom("/proc")
}

func listMCPProcessesFrom(procRoot string) ([]mcpProc, error) {
	ents, err := os.ReadDir(procRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errNoProc
		}
		return nil, err
	}
	var out []mcpProc
	for _, e := range ents {
		if !isProcPID(e.Name()) {
			continue
		}
		raw, rerr := os.ReadFile(filepath.Join(procRoot, e.Name(), "cmdline"))
		if rerr != nil || len(raw) == 0 {
			continue
		}
		args := parseCmdline(raw)
		if !isVPMCPServer(args) {
			continue
		}
		exe, lerr := os.Readlink(filepath.Join(procRoot, e.Name(), "exe"))
		if lerr != nil {
			exe = ""
		}
		out = append(out, mcpProc{PID: e.Name(), Exe: exe, Arg: args})
	}
	return out, nil
}

func isProcPID(name string) bool {
	if name == "" {
		return false
	}
	_, err := strconv.Atoi(name)
	return err == nil
}

func parseCmdline(raw []byte) []string {
	parts := bytes.Split(raw, []byte{0})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if len(p) == 0 {
			continue
		}
		out = append(out, string(p))
	}
	return out
}

// isVPMCPServer reports whether argv is a long-lived MCP server
// (`vp mcp` or `vp mcp serve`), not `vp mcp install` / `check` / a
// shell line that merely mentions those words.
func isVPMCPServer(args []string) bool {
	if len(args) < 2 {
		return false
	}
	if vpBase(args[0]) != "vp" {
		return false
	}
	i := nextPositional(args, 1)
	if i < 0 || args[i] != "mcp" {
		return false
	}
	j := nextPositional(args, i+1)
	if j < 0 {
		return true // `vp mcp`
	}
	return args[j] == "serve"
}

func vpBase(argv0 string) string {
	base := filepath.Base(argv0)
	return strings.TrimSuffix(base, deletedSuffix)
}

func nextPositional(args []string, i int) int {
	for i < len(args) {
		a := args[i]
		if a == "" || strings.HasPrefix(a, "-") {
			i++
			continue
		}
		return i
	}
	return -1
}
