// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/slug"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// resourceMIME is the MIME type for every vibe-palace content resource. All
// bodies (tasks, resume, workflow, commands, skills, sessions, knowledge) are
// markdown.
const resourceMIME = "text/markdown"

// resourceType describes one vibe-palace:// content resource: its URI template,
// the ordered path variables the template binds, and how to resolve a body from
// those variables. A single table drives the URI dispatch (ResolveURI) and the
// template registration (RegisterResources) so the resource surface is defined
// in exactly one place — adding a type is one table row, and per-variable
// validation cannot be forgotten because it is applied uniformly from `vars`.
//
// There are two distinct backend classes, and they differ in two contract-
// relevant ways:
//
//   - task / session / knowledge read the vault file DIRECTLY. The body is the
//     raw on-disk bytes (byte-identical to the file), and the read CAN miss
//     (a nonexistent task/session is an error; a project with no knowledge file
//     yet resolves to an empty body).
//
//   - resume / workflow / command / skill resolve through the precedence
//     Resolver. That means the body is NOT byte-identical to any single file:
//     the Resolver substitutes template placeholders ({{PROJECT}}, {{WING}},
//     {{ROOM}}, and {{DATE}} — which is today's date, so the same URI can yield
//     different bytes on different days), and it falls back to an embedded
//     default, so these types NEVER report not-found (a syntactically valid but
//     nonexistent project resolves to the embedded boilerplate, not an error).
//     Callers paging one of these via vp_read_resource should fetch it in a
//     single call where possible, since {{DATE}} makes mid-pagination bytes
//     non-deterministic across a midnight boundary.
type resourceType struct {
	name     string   // first URI segment, e.g. "task"
	template string   // RFC 6570 template, e.g. mcp.TaskURITemplate
	vars     []string // ordered var names the template binds, e.g. {"project","slug"}
	resolve  func(vars map[string]string, resolver *vpctx.Resolver, vault *storage.Vault) (string, error)
}

// resourceTypes is the single source of truth for the resource surface.
var resourceTypes = []resourceType{
	{
		name: "task", template: mcp.TaskURITemplate, vars: []string{"project", "slug"},
		resolve: func(v map[string]string, _ *vpctx.Resolver, vault *storage.Vault) (string, error) {
			_, body, err := vault.GetTask(v["project"], v["slug"])
			return body, err
		},
	},
	{
		name: "resume", template: mcp.ResumeURITemplate, vars: []string{"project"},
		resolve: func(v map[string]string, r *vpctx.Resolver, _ *storage.Vault) (string, error) {
			body, _, err := r.Resolve("resume", v["project"])
			return body, err
		},
	},
	{
		name: "workflow", template: mcp.WorkflowURITemplate, vars: []string{"project"},
		resolve: func(v map[string]string, r *vpctx.Resolver, _ *storage.Vault) (string, error) {
			body, _, err := r.Resolve("workflow", v["project"])
			return body, err
		},
	},
	{
		name: "command", template: mcp.CommandURITemplate, vars: []string{"project", "name"},
		resolve: func(v map[string]string, r *vpctx.Resolver, _ *storage.Vault) (string, error) {
			body, _, err := r.ResolveScoped("command:"+v["name"], v["project"], "", "")
			return body, err
		},
	},
	{
		name: "skill", template: mcp.SkillURITemplate, vars: []string{"project", "name"},
		resolve: func(v map[string]string, r *vpctx.Resolver, _ *storage.Vault) (string, error) {
			dir, _, err := r.ResolveSkillDir(v["name"], v["project"], "", "")
			if err != nil {
				return "", err
			}
			return string(dir.SkillMDBody), nil
		},
	},
	{
		name: "session", template: mcp.SessionURITemplate, vars: []string{"project", "session_id"},
		resolve: func(v map[string]string, _ *vpctx.Resolver, vault *storage.Vault) (string, error) {
			date, iter, err := parseSessionID(v["session_id"])
			if err != nil {
				return "", err
			}
			_, body, err := vault.ReadSession(v["project"], date, iter)
			return body, err
		},
	},
	{
		name: "knowledge", template: mcp.KnowledgeURITemplate, vars: []string{"project"},
		resolve: func(v map[string]string, _ *vpctx.Resolver, vault *storage.Vault) (string, error) {
			path, err := vault.KnowledgeFile(v["project"])
			if err != nil {
				return "", err
			}
			data, err := os.ReadFile(path)
			if errors.Is(err, fs.ErrNotExist) {
				// A project that has never recorded knowledge has no file yet —
				// that is the benign "not written yet" state, not an error.
				return "", nil
			}
			if err != nil {
				return "", fmt.Errorf("read knowledge: %w", err)
			}
			return string(data), nil
		},
	},
}

func resourceTypeByName(name string) *resourceType {
	for i := range resourceTypes {
		if resourceTypes[i].name == name {
			return &resourceTypes[i]
		}
	}
	return nil
}

// bindVars matches the path segments after the type to the template's variables,
// validating each against slug.Validate BEFORE it can reach the filesystem (the
// values arrive off the wire from resources/read and vp_read_resource). RFC 6570
// simple {var} expansion already won't match "/", but this is defense-in-depth
// against traversal and empty segments.
func (rt *resourceType) bindVars(pathSegs []string, uri string) (map[string]string, error) {
	if len(pathSegs) != len(rt.vars) {
		return nil, fmt.Errorf("malformed %s URI %q: want %s", rt.name, uri, rt.template)
	}
	vars := make(map[string]string, len(rt.vars))
	for i, name := range rt.vars {
		if err := slug.Validate(pathSegs[i]); err != nil {
			return nil, fmt.Errorf("invalid %s in %q: %w", name, uri, err)
		}
		vars[name] = pathSegs[i]
	}
	return vars, nil
}

// ResolveURI resolves a vibe-palace:// content URI to its body and MIME type. It
// strips the scheme, splits the path, looks up the resource type, validates the
// bound variables (constraint: before any filesystem access), then delegates to
// the type's resolve func. See resourceType for the two backend classes and
// their byte-identity / not-found contracts. An unknown scheme or resource type
// yields a distinct error from a content-not-found error (the latter bubbles up
// from the backing vault/resolver call).
func ResolveURI(uri string, resolver *vpctx.Resolver, vault *storage.Vault) (text, mime string, err error) {
	rest, ok := strings.CutPrefix(uri, mcp.ResourceScheme)
	if !ok {
		return "", "", fmt.Errorf("unknown resource URI %q: missing %q scheme", uri, mcp.ResourceScheme)
	}
	segs := strings.Split(rest, "/")
	if len(segs) == 0 || segs[0] == "" {
		return "", "", fmt.Errorf("unknown resource URI %q: empty resource type", uri)
	}
	rt := resourceTypeByName(segs[0])
	if rt == nil {
		return "", "", fmt.Errorf("unknown resource type %q in URI %q", segs[0], uri)
	}
	vars, err := rt.bindVars(segs[1:], uri)
	if err != nil {
		return "", "", err
	}
	body, err := rt.resolve(vars, resolver, vault)
	if err != nil {
		return "", "", err
	}
	return body, resourceMIME, nil
}

// RegisterResources wires every content-resource provider onto srv from the
// resourceTypes table. Each provider closes over the captured resolver/vault
// (NEVER reads them from ctx — the streamable-HTTP transport injects no
// contextFunc), validates the template-matched vars, and resolves directly. It
// does NOT rebuild an URI string and re-parse it through ResolveURI: the vars
// are already in hand from the matched template, so the dispatch is a direct
// table lookup.
func RegisterResources(srv *mcp.Server, resolver *vpctx.Resolver, vault *storage.Vault) {
	for i := range resourceTypes {
		rt := &resourceTypes[i]
		srv.AddContentResource(rt.template, rt.name, resourceMIME,
			func(_ context.Context, vars map[string]string) (string, string, error) {
				for _, name := range rt.vars {
					if err := slug.Validate(vars[name]); err != nil {
						return "", "", fmt.Errorf("invalid %s: %w", name, err)
					}
				}
				body, err := rt.resolve(vars, resolver, vault)
				if err != nil {
					return "", "", err
				}
				return body, resourceMIME, nil
			})
	}
}
