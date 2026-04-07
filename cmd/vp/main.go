// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"context"
	"fmt"
	"os"

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	mcpkg "github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/tools"
)

func main() {
	v, err := storage.OpenVault("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "vp: %v\n", err)
		os.Exit(1)
	}
	resolver := vpctx.NewResolver(v.Root)
	srv := mcpkg.NewServer(v)
	tools.RegisterAll(srv.Registry(), resolver, v)
	if err := srv.Serve(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "vp: %v\n", err)
		os.Exit(1)
	}
}
