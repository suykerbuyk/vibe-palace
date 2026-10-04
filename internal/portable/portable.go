// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package portable holds the portable-filename rule: whether a path segment
// can be represented on every filesystem a shipped release target uses
// (NTFS, exFAT, APFS, ext4). It is a leaf, so both internal/vaultfs and
// internal/slug can use it without either importing the other.
package portable

import (
	"errors"
	"fmt"
	"strings"
)

// ErrUnportableName is returned when a path segment cannot be represented on
// every shipped release target's filesystem: a reserved character, a trailing
// dot or space, a Windows reserved device name, or an over-length segment.
// vaultfs.ErrUnportableName is this same value.
var ErrUnportableName = errors.New("vaultfs: unportable filename rejected")

// portableReservedChars are the characters no NTFS/exFAT filename may contain.
// The path separator "/" is deliberately absent — it delimits segments and is
// handled structurally by ValidateRelPath. A literal backslash IS rejected: it is
// a separator on Windows, so a "\" inside a vault-relative segment would change
// the path's meaning on checkout. Control characters are rejected separately by
// ValidateRelPath.
const portableReservedChars = `<>:"\|?*`

// reservedDeviceNames are the Windows reserved device basenames. A file whose
// name (or whose stem before the first ".") equals one of these — case-insensitively,
// with or without an extension, e.g. "NUL", "nul.md", "COM1.txt" — cannot be
// created or checked out on Windows.
var reservedDeviceNames = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true,
	"com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true,
	"lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// ValidatePortableSegment reports whether a single path segment — one filename or
// directory name, never a multi-segment path — is representable on NTFS/exFAT.
// It is the ONE definition of "portable name" shared by the write path
// (ValidateRelPath) and the vaultaudit memory-portability dimension, so the
// audit flags exactly what a write would refuse and the two cannot drift.
//
// Rejects (all with ErrUnportableName): reserved characters, a Windows reserved
// device name, a trailing dot or space (Windows silently strips these, so the
// name you wrote is not the name on disk), and a segment over 255 bytes (the
// common per-component filesystem limit).
func ValidateSegment(seg string) error {
	if seg == "" {
		return fmt.Errorf("%w: empty segment", ErrUnportableName)
	}
	if i := strings.IndexAny(seg, portableReservedChars); i >= 0 {
		return fmt.Errorf("%w: segment %q contains %q, illegal on NTFS/exFAT",
			ErrUnportableName, seg, seg[i])
	}
	if last := seg[len(seg)-1]; last == '.' || last == ' ' {
		return fmt.Errorf("%w: segment %q ends with a dot or space, illegal on Windows",
			ErrUnportableName, seg)
	}
	stem, _, _ := strings.Cut(seg, ".")
	if reservedDeviceNames[strings.ToLower(stem)] {
		return fmt.Errorf("%w: segment %q is a Windows reserved device name",
			ErrUnportableName, seg)
	}
	if len(seg) > 255 {
		return fmt.Errorf("%w: segment is %d bytes, over the 255-byte filesystem limit",
			ErrUnportableName, len(seg))
	}
	return nil
}
