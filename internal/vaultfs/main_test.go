// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultfs

import (
	"os"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

func TestMain(m *testing.M) { os.Exit(testutil.RunHermetic(m)) }
