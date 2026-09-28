// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"fmt"
	"os"
	"strings"
)

// stampedCommitMessage is a vp commit message: message, then the "[hostname]"
// stamp paragraph, then — when trailers is set — the trailer block as the
// FINAL paragraph.
//
// 🔴 THE TRAILERS MUST COME LAST. git reads trailers only from a message's
// last paragraph (git interpret-trailers --parse), so a trailer block with the
// stamp after it parses as no trailers at all, and every trailer search
// (commitsWithTrailer) then misses the commit. With no trailers the message is
// byte-for-byte the old "message\n\n[hostname]".
func stampedCommitMessage(message, trailers string) string {
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "unknown"
	}
	msg := fmt.Sprintf("%s\n\n[%s]", message, hostname)
	if t := strings.TrimSpace(trailers); t != "" {
		msg += "\n\n" + t
	}
	return msg
}
