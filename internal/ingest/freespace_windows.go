// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build windows

package ingest

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// platformFreeSpace reads free bytes via GetDiskFreeSpaceEx. Windows reports no
// inode count, so HasInodes is false and the watchdog guards bytes only there.
type platformFreeSpace struct{}

func (platformFreeSpace) Free(path string) (FreeSpace, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return FreeSpace{}, fmt.Errorf("ingest: path %s: %w", path, err)
	}
	var freeToCaller, totalBytes, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &freeToCaller, &totalBytes, &totalFree); err != nil {
		return FreeSpace{}, fmt.Errorf("ingest: GetDiskFreeSpaceEx %s: %w", path, err)
	}
	return FreeSpace{Bytes: freeToCaller, HasInodes: false}, nil
}

func newPlatformFreeSpace() FreeSpaceReader { return platformFreeSpace{} }
