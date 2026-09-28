// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

import (
	"go/ast"
	"sort"
	"strconv"
	"strings"
)

// departureRecordWriter pins WHO may write or remove a departure record.
//
// # Why this rule exists
//
// Under the record-wins rule (task lc-u15-vaultfs-refuses-writes-into-a-departed-project)
// Audits/departures/<p>.json is the most powerful file in the vault: forging
// one locks a live project out of every writer, and removing one reopens a
// departed project in the vault it left. The write funnel refuses every
// ordinary change there; only the lifecycle commands pass, through a few
// privileged entry points that demand the vault's live root-lock token. The
// token proves the caller holds the vault — not that the caller SHOULD be
// writing records. This rule pins that second fact.
//
// It reports every reference — a call, or the function taken as a value — in
// non-test code to one of the privileged entry points (departureRecordEntries),
// keyed on the enclosing function: `<package>.<func>`. EVERY such caller is a
// finding, and each allowed one is a reviewed baseline entry naming why. A new
// caller therefore fails the gate until someone rules on it in writing.
//
// # What it cannot see, stated
//
// It is AST-only and name-based. vaultfs's and atomicfile's entry points are
// matched as selectors on the file's import of that package (or unqualified
// inside it); the storage methods are matched by method name on any receiver,
// which over-reports rather than under-reports. The raw removal and rename
// sinks a hand-rolled record change would need — vaultfs.RemoveNoLock and
// vaultfs.RenameNoLock — and the raw os.* writes are NOT pinned here: they
// remain covered by the vault-write funnel audit (vaultWriteFunnel), whose
// sinks they are.
func departureRecordWriter(files []file) []Finding {
	type hit struct{ pos, detail string }
	seen := map[string]hit{}
	var order []string
	found := map[string]bool{}
	for _, f := range files {
		if f.isTest {
			continue
		}
		pkg := f.ast.Name.Name
		imports := map[string]string{} // local name -> owner package ("vaultfs", "atomicfile")
		for _, im := range f.ast.Imports {
			p, _ := strconv.Unquote(im.Path.Value)
			for _, owner := range []string{"vaultfs", "atomicfile"} {
				if strings.HasSuffix(p, "/internal/"+owner) {
					name := owner
					if im.Name != nil {
						name = im.Name.Name
					}
					imports[name] = owner
				}
			}
		}
		for _, decl := range f.ast.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			scope := pkg + "." + funcName(fn)
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				var entry string
				switch x := n.(type) {
				case *ast.SelectorExpr:
					if id, ok := x.X.(*ast.Ident); ok {
						if owner, ok := imports[id.Name]; ok && departureRecordEntries[owner+"."+x.Sel.Name] {
							entry = owner + "." + x.Sel.Name
						}
					}
					if entry == "" && departureRecordEntries["storage.Vault."+x.Sel.Name] {
						entry = "storage.Vault." + x.Sel.Name
					}
				case *ast.Ident:
					if departureRecordEntries[pkg+"."+x.Name] && fn.Name.Name != x.Name {
						entry = pkg + "." + x.Name
					}
				}
				if entry == "" {
					return true
				}
				found[entry] = true
				if _, dup := seen[scope]; !dup {
					order = append(order, scope)
				}
				seen[scope] = hit{pos: f.fset.Position(n.Pos()).String(), detail: entry}
				return true
			})
		}
	}
	sort.Strings(order)
	var out []Finding
	for _, scope := range order {
		h := seen[scope]
		out = append(out, Finding{
			Kind:   KindDepartureRecordWriter,
			Symbol: scope,
			Pos:    h.pos,
			Detail: "reaches " + h.detail + ", a privileged departure-record entry point. Only the vault lifecycle commands write or remove records; name this caller in baseline.json WITH A REASON, or route the change through a lifecycle command",
		})
	}
	// Vacuity: if the one record writer is renamed away, this rule would pass
	// on nothing. Say so instead.
	if !found["vaultfs.WriteDepartureRecord"] {
		out = append(out, Finding{Kind: KindDepartureRecordWriter, Symbol: "sourceaudit.departureRecordWriter/ABSENT/vaultfs.WriteDepartureRecord",
			Detail: "no caller of vaultfs.WriteDepartureRecord was found: the rule is vacuous; update departureRecordEntries to the record writer's new name"})
	}
	return out
}

// departureRecordEntries are the privileged entry points, keyed
// "<owner>.<name>".
var departureRecordEntries = map[string]bool{
	"vaultfs.WriteDepartureRecord":           true,
	"vaultfs.RemoveDepartureRecord":          true,
	"atomicfile.ForDepartureRecord":          true,
	"storage.Vault.RecordDeparture":          true,
	"storage.Vault.RecordDepartureForPurge":  true,
	"storage.Vault.RecordDepartureForDelete": true,
}
