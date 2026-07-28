// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	mcpkg "github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/tools"
)

// defaultBearerTokenEnv is the environment variable consulted for the HTTP
// server's bearer token when --bearer-token-env is not overridden. Only the
// variable NAME is configurable; the token VALUE lives solely in the
// environment and is never read from config.
const defaultBearerTokenEnv = "VP_MCP_BEARER_TOKEN"

var mcpServeFlags = []cli.FlagDef{
	{Name: "--port", Short: "-P", Arg: "N", Help: "HTTP port", Default: "7423"},
	{Name: "--addr", Arg: "HOST", Help: "Host/interface to bind", Default: "127.0.0.1"},
	{Name: "--allow-writes", Help: "Expose vault-mutating tools (read-only by default)"},
	{Name: "--bearer-token-env", Arg: "ENV", Help: "Env var holding the bearer token", Default: defaultBearerTokenEnv},
}

func cmdMCPServe() *cli.Command {
	return &cli.Command{
		Name:     "mcp serve",
		Synopsis: "vp mcp serve [--port N] [--addr HOST] [--allow-writes] [--bearer-token-env ENV]",
		Description: "Run a bearer-authenticated, read-only-by-default MCP server over " +
			"Streamable HTTP on a dedicated server instance. The bearer token is read " +
			"from the environment variable named by --bearer-token-env (default " +
			"VP_MCP_BEARER_TOKEN); when that variable is unset the server runs " +
			"UNAUTHENTICATED and prints a warning — front it with a tunnel or network " +
			"ACL you control. By default only read tools are exposed; pass --allow-writes " +
			"to also expose the vault-mutating tools. Binds 127.0.0.1 by default; public " +
			"exposure is expected to go through an explicit tunnel.",
		Flags: mcpServeFlags,
		Examples: []cli.Example{
			{Cmd: "vp mcp serve", Comment: "Read-only server on 127.0.0.1:7423"},
			{Cmd: "VP_MCP_BEARER_TOKEN=secret vp mcp serve", Comment: "Require a bearer token"},
			{Cmd: "vp mcp serve --allow-writes", Comment: "Also expose vault-mutating tools"},
			{Cmd: "vp mcp serve --addr 0.0.0.0 --port 9000", Comment: "Bind all interfaces on a custom port"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(mcpServeFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp mcp serve: %v\n", err)
				return cli.ExitUser
			}

			stack, err := bootstrap()
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp mcp serve: %v\n", err)
				return cli.ExitSystem
			}
			defer stack.close()

			sc := resolveMCPServeConfig(fv, stack.cfg)
			handler := buildMCPServeHandler(stack, sc.token, sc.allowWrites)

			for _, line := range sc.startupLines() {
				fmt.Fprintln(os.Stderr, line)
			}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			if err := serveHTTP(ctx, sc.addr, handler); err != nil {
				fmt.Fprintf(os.Stderr, "vp mcp serve: %v\n", err)
				return cli.ExitSystem
			}
			return cli.ExitOK
		},
	}
}

// mcpServeConfig is the resolved runtime configuration for `vp mcp serve`,
// derived from parsed flags and the loaded config. It is computed by a pure
// function so the flag precedence and warning logic are testable without a
// vault or a bound socket.
type mcpServeConfig struct {
	addr        string // host:port to bind
	token       string // bearer token value (resolved from the env var)
	tokenEnv    string // name of the env var consulted for the token
	allowWrites bool   // expose vault-mutating tools when true
}

// resolveMCPServeConfig applies the flag/precedence rules: --port falls back to
// cfg.HTTPPort then 7423; --addr defaults to 127.0.0.1; the bearer token is read
// from the env var named by --bearer-token-env (default VP_MCP_BEARER_TOKEN).
func resolveMCPServeConfig(fv *cli.FlagValues, cfg storage.Config) mcpServeConfig {
	port := fv.Get("--port")
	if port == "" {
		if cfg.HTTPPort != 0 {
			port = fmt.Sprintf("%d", cfg.HTTPPort)
		} else {
			port = "7423"
		}
	}

	host := fv.Get("--addr")
	if host == "" {
		host = "127.0.0.1"
	}

	tokenEnv := fv.Get("--bearer-token-env")
	if tokenEnv == "" {
		tokenEnv = defaultBearerTokenEnv
	}

	return mcpServeConfig{
		addr:        host + ":" + port,
		token:       os.Getenv(tokenEnv),
		tokenEnv:    tokenEnv,
		allowWrites: fv.Bool("--allow-writes"),
	}
}

// startupLines returns the stderr lines to print at startup: a loud warning
// when the server is unauthenticated, a loud warning when writes are exposed,
// and the listening/mode line. Returning them as a slice keeps the side-effect
// (writing to stderr) out of the testable logic.
func (c mcpServeConfig) startupLines() []string {
	var lines []string
	if c.token == "" {
		lines = append(lines, fmt.Sprintf(
			"WARNING: no bearer token set (%s unset) — the MCP HTTP server is UNAUTHENTICATED",
			c.tokenEnv))
	}
	if c.allowWrites {
		lines = append(lines,
			"WARNING: --allow-writes is set — vault-mutating tools are exposed over the network")
	}
	mode := "read-only"
	if c.allowWrites {
		mode = "read-write"
	}
	lines = append(lines, fmt.Sprintf(
		"MCP Streamable-HTTP server listening on http://%s (%s)", c.addr, mode))
	return lines
}

// buildMCPServeHandler constructs the HTTP handler for `vp mcp serve` on a
// DEDICATED MCP server instance (never the stdio one). It registers the full
// tool set, then — unless allowWrites — strips the vault-mutating tools from
// this instance so they are absent from tools/list and tools/call. The handler
// enforces bearer-token auth when token is non-empty. Extracted from Run so the
// exact production handler (real RegisterAll tool set, real filtering) is
// testable without binding a socket.
func buildMCPServeHandler(stack *serverStack, token string, allowWrites bool) http.Handler {
	srv := mcpkg.NewServer(stack.vault)
	// Every transport now gets the SAME payload. This instance used to default
	// vp_bootstrap_context to a 4,000-byte resume prefix on the claim that "the
	// streamable-HTTP channel truncates large inline tool results" — never
	// measured, and the reduction was invisible in the response. Deleted; the
	// token ladder is the one reduction path and it reports what it drops.
	// Require explicit project on bootstrap: HTTP serve multiplexes clients
	// over one long-lived process whose cwd is the operator's launch dir, not
	// a per-call project. Stdio MCP (vp mcp) keeps high-confidence cwd defaulting.
	tools.RegisterAll(srv.Registry(), stack.resolver, stack.vault, stack.eng,
		tools.WithConfig(stack.cfg),
		tools.WithRequireExplicitProject())
	tools.RegisterResources(srv, stack.resolver, stack.vault)
	if !allowWrites {
		srv.DeleteTools(tools.MutatingToolNames...)
	}
	return srv.StreamableHTTPHandler(token)
}

// serveHTTP runs handler on addr until ctx is cancelled, then drains in-flight
// requests via graceful shutdown. A clean shutdown returns nil.
func serveHTTP(ctx context.Context, addr string, handler http.Handler) error {
	httpSrv := &http.Server{Addr: addr, Handler: handler}

	go func() {
		<-ctx.Done()
		httpSrv.Shutdown(context.Background())
	}()

	err := httpSrv.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
