// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
)

func cmdMCP() *cli.Command {
	return &cli.Command{
		Name:        "mcp",
		Synopsis:    "vp mcp",
		Description: "Start the MCP server on stdio (JSON-RPC).",
		Run: func(args []string) int {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return serveMCP(ctx, os.Stdin, os.Stdout)
		},
	}
}

// serveMCP runs the MCP stdio server. Blocks until ctx is cancelled or EOF.
func serveMCP(ctx context.Context, in io.Reader, out io.Writer) int {
	stack, err := bootstrap()
	if err != nil {
		fmt.Fprintf(os.Stderr, "vp: %v\n", err)
		return cli.ExitSystem
	}
	defer stack.close()

	if err := stack.srv.Listen(ctx, in, out); err != nil {
		fmt.Fprintf(os.Stderr, "vp: %v\n", err)
		return cli.ExitSystem
	}
	return cli.ExitOK
}
