// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

import (
	"go/ast"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// indexRecordStructs are the record shapes of the host-local index: the KG
// payload builders' structs and the store's own line and ledger records.
var indexRecordStructs = map[string][]string{
	"internal/index/":      {"TriplePayload", "EntityPayload"},
	"internal/indexstore/": {"kgLine", "chunkLine", "ledgerRecord"},
}

// projectFields lists every field of the named structs, in the given sources,
// that is named project or tagged json:"project", and every named struct that
// was not found at all.
func projectFields(srcs []goSource, want map[string][]string) (bad, missing []string) {
	found := map[string]bool{}
	for _, s := range srcs {
		for dir, names := range want {
			if !strings.HasPrefix(s.rel, dir) || strings.Count(strings.TrimPrefix(s.rel, dir), "/") != 0 {
				continue
			}
			for _, decl := range s.file.Decls {
				ast.Inspect(decl, func(n ast.Node) bool {
					ts, ok := n.(*ast.TypeSpec)
					if !ok {
						return true
					}
					st, ok := ts.Type.(*ast.StructType)
					if !ok {
						return true
					}
					for _, name := range names {
						if ts.Name.Name != name {
							continue
						}
						found[dir+name] = true
						for _, f := range st.Fields.List {
							var tag string
							if f.Tag != nil {
								raw, _ := strconv.Unquote(f.Tag.Value)
								tag = reflect.StructTag(raw).Get("json")
							}
							jsonName, _, _ := strings.Cut(tag, ",")
							for _, fn := range f.Names {
								if strings.EqualFold(fn.Name, "project") || jsonName == "project" {
									bad = append(bad, s.rel+": "+name+"."+fn.Name)
								}
							}
						}
					}
					return true
				})
			}
		}
	}
	for dir, names := range want {
		for _, n := range names {
			if !found[dir+n] {
				missing = append(missing, dir+n)
			}
		}
	}
	return bad, missing
}

// R10: no host-local index record carries a project slug; the project is the
// index directory, so a rename or copy moves records without rewriting them.
// The check parses source, so internal/indexstore needs no change for it.
func TestIndexRecordsCarryNoProject(t *testing.T) {
	bad, missing := projectFields(moduleSources(t), indexRecordStructs)
	for _, m := range missing {
		t.Errorf("record struct %s not found: the check would pass vacuously", m)
	}
	for _, b := range bad {
		t.Errorf("%s: an index record carries a project field", b)
	}
}
