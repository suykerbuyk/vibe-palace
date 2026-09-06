// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package surface

import (
	"os"
	"strings"
)

// selfExeLink is the procfs symlink naming this process's executable image.
// Tests replace readSelfExe rather than this, so the constant stays honest
// about what is being read on a real host.
const selfExeLink = "/proc/self/exe"

// readSelfExe resolves this process's own executable image. Tests replace it.
//
// os.Readlink is deliberate: os.Executable() resolves the link and hands back a
// cleaned path with the " (deleted)" marker STRIPPED, which is precisely the
// signal this package exists to read. Reading the raw link is the only way to
// see it.
var readSelfExe = func() (string, error) { return os.Readlink(selfExeLink) }

// deletedImageSuffix is the marker Linux appends to /proc/<pid>/exe once the
// inode the process is executing has been unlinked. internal/check's process
// scanner keys on the same string for other processes; this file answers the
// same question about THIS one.
const deletedImageSuffix = " (deleted)"

// SelfImageReplaced reports whether the executable THIS process is running has
// been unlinked since the process started — the shape `make install` produces
// when it replaces the inode under a long-lived `vp mcp` server.
//
// It answers a deliberately NARROWER question than internal/check's stale-MCP
// scan. That check surveys the whole process table so an operator can see every
// stale server on the machine; this one asks only "is the image *I* am about to
// write the vault with the one that is installed?", because that is the process
// whose write paths are actually in play. A scan is the right shape for a
// report and the wrong shape for a per-write predicate: it costs a /proc walk,
// and it can answer "yes, stale" about a server that is not this one.
//
// The returned string is the raw link target, marker included, so a caller can
// name the image in an operator-facing message without re-reading it.
//
// It is CHEAP — one readlink, no directory walk, no exec — and it is quiet:
// every failure returns false. A host with no /proc (macOS, Windows, a
// container without procfs) reports false, which is the honest answer, because
// this signal is not available there rather than absent.
//
// 🔴 Never gate a write on this. The condition it reports is advisory by
// operator ruling: the operator is told his tooling may not do the right thing
// and is left free to proceed. Refusing here would lose work to protect a
// schema, which is the trade internal/surface/gate.go already refuses to make
// for `vp hook`.
func SelfImageReplaced() (bool, string) {
	exe, err := readSelfExe()
	if err != nil {
		return false, ""
	}
	if !strings.HasSuffix(exe, deletedImageSuffix) {
		return false, ""
	}
	return true, exe
}

// StaleBinaryAdvisory renders the operator-facing notice for a vault write made
// by a process whose image has been replaced.
//
// It states the outcome FIRST — the write succeeded — because the operator
// ruling this implements is "advise, then let him decide", and a notice that
// opens with the problem reads as a refusal. It then names both choices
// explicitly, because "you may proceed" is not information unless the way to
// stop is stated beside it.
//
// image is the raw link target from SelfImageReplaced.
func StaleBinaryAdvisory(image string) string {
	return "⚠ THIS WRITE SUCCEEDED, AND THIS BINARY MAY NOT HAVE DONE THE RIGHT THING.\n\n" +
		"This vp server is executing an image that has been replaced on disk:\n" +
		"    " + image + "\n" +
		"`make install` swapped the inode while this process kept running the old one, so\n" +
		"every vault write from this session uses the OLD write paths — which may not match\n" +
		"what the installed binary would have written.\n\n" +
		"Nothing has been refused and nothing will be: losing work to protect a schema is the\n" +
		"wrong trade. You are being told so that YOU can decide.\n\n" +
		"    To stop and pick up the new binary:  restart this AI host.\n" +
		"        Killing the server alone is not enough — that leaves the session with no\n" +
		"        vp_* tools at all.\n" +
		"    To proceed anyway:  keep working. This notice does not repeat.\n\n" +
		"Reported once per server process. Re-derive at any time with\n" +
		"`vp check --check stale-mcp`, which surveys every vp server on this machine."
}
