// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package mcp

import (
	"context"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// ResourceFunc resolves a content resource from the template-matched URI
// variables. It returns the resource body, its MIME type, and any error.
//
// Handlers MUST NOT read the *storage.Vault or *context.Resolver from ctx.
// Providers close over the vault/resolver captured at registration time
// instead. (The vault IS now on the context over both transports — see
// StreamableHTTPHandler — but the resolver is not, and closing over both keeps
// one rule rather than two.)
type ResourceFunc func(ctx context.Context, vars map[string]string) (text, mime string, err error)

// AddContentResource registers a read-only content resource template with the
// underlying MCP server. It mirrors Registry for tools: the generic primitive
// lives here in internal/mcp, while the concrete providers are wired from
// internal/tools (which may import internal/mcp but not vice versa). Because
// Server.mcp is unexported, this exported method is the only way an
// out-of-package caller can register a resource.
//
// fn receives the URI variables matched from uriTemplate (mcp-go populates
// request.Params.Arguments from the template's {var} slots). The returned MIME
// type overrides the registration default when non-empty.
func (s *Server) AddContentResource(uriTemplate, name, mime string, fn ResourceFunc) {
	tmpl := mcplib.NewResourceTemplate(uriTemplate, name, mcplib.WithTemplateMIMEType(mime))

	handler := func(ctx context.Context, req mcplib.ReadResourceRequest) ([]mcplib.ResourceContents, error) {
		// mcp-go fills Arguments from the matched URI template. Each value is
		// the underlying uritemplate Value.V, a []string (one element for a
		// simple {var} slot); older/other paths may hand us a plain string.
		// Coerce both to the first scalar.
		vars := make(map[string]string, len(req.Params.Arguments))
		for k, v := range req.Params.Arguments {
			switch sv := v.(type) {
			case string:
				vars[k] = sv
			case []string:
				if len(sv) > 0 {
					vars[k] = sv[0]
				}
			case []any:
				if len(sv) > 0 {
					if s, ok := sv[0].(string); ok {
						vars[k] = s
					}
				}
			}
		}

		text, m, err := fn(ctx, vars)
		if err != nil {
			return nil, err
		}
		if m == "" {
			m = mime
		}

		return []mcplib.ResourceContents{
			mcplib.TextResourceContents{
				URI:      req.Params.URI,
				MIMEType: m,
				Text:     text,
			},
		}, nil
	}

	s.mcp.AddResourceTemplate(tmpl, server.ResourceTemplateHandlerFunc(handler))
}
