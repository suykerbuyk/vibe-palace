// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build !unix

package vaultfs

import "errors"

func mkfifo(string) error { return errors.New("no FIFOs on this platform") }
