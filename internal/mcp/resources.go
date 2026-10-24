// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package mcp

import (
	"context"
	"encoding/json"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// ResourceFunc resolves a content resource from the template-matched URI
// variables. It returns the resource body, its MIME type, and any error.
//
// Handlers read their *storage.Vault / *context.Resolver ctx-first: the
// request's bound VaultStack when AddContentResource installed one (keyed on the
// project var, mirroring tool dispatch), else the vault/resolver they captured at
// registration. This is the same ctx-first rule the tool handlers follow, so a
// native resources/read routes to the bound vault on a multi-vault host instead
// of the startup vault.
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

		// Route the read to the request's bound vault: synthesize the project-keyed
		// params the dispatch resolver already understands and install the resolved
		// VaultStack on ctx, exactly as makeHandler/Dispatch do for tool calls. The
		// provider then reads its vault/resolver ctx-first (resolverFromCtx /
		// vaultFromCtx). Keyed on the `project` var — the same param-name convention
		// the mutating gate uses — so a native resources/read agrees with a routed
		// tool read on a multi-vault host. A read is read-only, so no gate runs here.
		// learning carries no project var: install is skipped and it serves startup.
		if proj := vars["project"]; proj != "" {
			params, merr := json.Marshal(struct {
				Project string `json:"project"`
			}{Project: proj})
			if merr != nil {
				return nil, merr
			}
			enriched, serr := s.registry.installStack(ctx, json.RawMessage(params))
			if serr != nil {
				return nil, serr
			}
			ctx = enriched
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
