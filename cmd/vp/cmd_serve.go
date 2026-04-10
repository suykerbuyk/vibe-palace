// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	mcpkg "github.com/suykerbuyk/vibe-palace/internal/mcp"
)

var serveFlags = []cli.FlagDef{
	{Name: "--port", Short: "-P", Arg: "N", Help: "HTTP port", Default: "7423"},
}

func cmdServe() *cli.Command {
	return &cli.Command{
		Name:        "serve",
		Synopsis:    "vp serve [--port N]",
		Description: "Start the HTTP REST server for tool access. Exposes MCP tools over HTTP for integration with editors and other clients.",
		Flags:       serveFlags,
		Examples: []cli.Example{
			{Cmd: "vp serve", Comment: "Start on default port 7423"},
			{Cmd: "vp serve --port 9000", Comment: "Start on a custom port"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(serveFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp serve: %v\n", err)
				return cli.ExitUser
			}

			stack, err := bootstrap()
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp serve: %v\n", err)
				return cli.ExitSystem
			}
			defer stack.close()

			port := fv.Get("--port")
			if port == "" {
				if stack.cfg.HTTPPort != 0 {
					port = fmt.Sprintf("%d", stack.cfg.HTTPPort)
				} else {
					port = "7423"
				}
			}

			addr := ":" + port
			fmt.Fprintf(os.Stderr, "Listening on http://localhost%s\n", addr)

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			httpSrv := mcpkg.NewHTTPServer(stack.srv, addr)
			if err := httpSrv.ListenAndServe(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "vp serve: %v\n", err)
				return cli.ExitSystem
			}
			return cli.ExitOK
		},
	}
}
