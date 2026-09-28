// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// shellWords runs line through sh -c with its leading "vp" replaced by a
// printer, and returns the words the shell handed that command: what vp would
// receive if the operator pasted the line.
func shellWords(t *testing.T, line string) []string {
	t.Helper()
	rest, ok := strings.CutPrefix(line, "vp ")
	if !ok {
		t.Fatalf("line %q does not start with vp", line)
	}
	out, err := exec.Command("sh", "-c", `printf '%s\0' `+rest).Output()
	if err != nil {
		t.Fatalf("sh -c on %q: %v", line, err)
	}
	return strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
}

// Every lifecycle command's printed real-run and re-run line survives a paste
// into a shell: values holding a space, a quote or a shell metacharacter
// reach vp as single, unchanged arguments.
func TestPrintedLinesRunAsPrinted(t *testing.T) {
	path := "/home/o'neil/my vaults/personal $HOME vault"
	url := "file:///srv/git/quantum vault.git"
	cases := map[string]struct {
		line string
		want []string
	}{
		"delete --moved-to": {
			DeleteRequest{Projects: []string{"qms", "orch"}, MovedTo: url}.commandLine(path, "v1:abc"),
			[]string{"vault", "project", "delete", "qms", "orch", "--moved-to", url, "--vault", path, "--expect", "v1:abc"},
		},
		"delete --discard": {
			DeleteRequest{Projects: []string{"qms"}, Discard: true}.commandLine(path, ""),
			[]string{"vault", "project", "delete", "qms", "--discard", "--vault", path},
		},
		"init re-run": {
			initRerun(path, []RecordedRemote{{Name: "origin", URL: url}}),
			[]string{"vault", "init", path, "--remote", "origin=" + url},
		},
		"copy": {
			CopyCommandLine(&CopyPlan{Projects: []CopyProject{{Slug: "qms"}}, Source: url, At: "abc", Vault: path, Digest: "d"}),
			[]string{"vault", "copy", "qms", "--from", url, "--at", "abc", "--vault", path, "--expect", "d"},
		},
		"copy re-run": {
			copyRerunLine(path, []string{"qms"}, url),
			[]string{"vault", "copy", "qms", "--from", url, "--vault", path},
		},
		"clone": {
			cloneCommandLine(CloneRequest{URL: url, Bind: []string{"qms", "orch"}}, path, "d"),
			[]string{"vault", "clone", url, path, "--bind", "qms", "orch", "--expect", "d"},
		},
	}
	for name, c := range cases {
		if got := shellWords(t, c.line); !slices.Equal(got, c.want) {
			t.Errorf("%s: the line %q reaches vp as\n  %q\nwant\n  %q", name, c.line, got, c.want)
		}
	}
}
