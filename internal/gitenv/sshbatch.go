// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package gitenv

import (
	"regexp"
	"strings"
)

// batchModeOpt is the ssh option that makes ssh fail instead of prompting for
// a passphrase, a password or a host key. GIT_TERMINAL_PROMPT=0 stops git's own
// credential prompt but not ssh's, so a network call without it can hang on a
// terminal nobody is watching.
const batchModeOpt = "-o BatchMode=yes"

// SSHBatchModeOverride composes ssh BatchMode for a git subprocess WITHOUT
// replacing the operator's own ssh command. env is the environment the
// subprocess would otherwise get (os.Environ()); the result is the variables
// to append after it — os/exec keeps the last value of a duplicated key — and
// any `-c` arguments to put before the git subcommand.
//
//  1. GIT_SSH_COMMAND is set: BatchMode is appended to it, so an operator's
//     `ssh -i <key> -o IdentitiesOnly=yes -o IdentityAgent=none` survives.
//  2. Else core.sshCommand (the caller looks it up and passes it) is set: it
//     is passed back as `-c core.sshCommand=<that> -o BatchMode=yes`.
//  3. Else GIT_SSH is set: nothing changes. GIT_SSH names a program, not a
//     command line (it may be plink), so there is no safe place to add an ssh
//     option, and setting GIT_SSH_COMMAND would silently override it.
//  4. Else GIT_SSH_COMMAND='ssh -o BatchMode=yes'.
//
// A command that already passes a BatchMode option is left as it is.
func SSHBatchModeOverride(env []string, coreSSHCommand string) (override []string, configArgs []string) {
	var sshCommand, gitSSH string
	for _, kv := range env {
		name, val, _ := strings.Cut(kv, "=")
		switch name {
		case "GIT_SSH_COMMAND":
			sshCommand = val
		case "GIT_SSH":
			gitSSH = val
		}
	}
	switch {
	case strings.TrimSpace(sshCommand) != "":
		return []string{"GIT_SSH_COMMAND=" + appendBatchMode(sshCommand)}, nil
	case strings.TrimSpace(coreSSHCommand) != "":
		return nil, []string{"-c", "core.sshCommand=" + appendBatchMode(coreSSHCommand)}
	case gitSSH != "":
		return nil, nil
	default:
		return []string{"GIT_SSH_COMMAND=ssh " + batchModeOpt}, nil
	}
}

// hasBatchModeOpt matches an ssh BatchMode OPTION (`-o BatchMode=yes`,
// `-oBatchMode no`; ssh keywords are case-insensitive), never the word inside
// a path: a key or a directory named "...BatchMode..." must not suppress it.
var hasBatchModeOpt = regexp.MustCompile(`(?i)(^|\s)-o\s*batchmode(\s*=|\s)`)

func appendBatchMode(cmd string) string {
	if hasBatchModeOpt.MatchString(cmd) {
		return cmd
	}
	return strings.TrimRight(cmd, " ") + " " + batchModeOpt
}
