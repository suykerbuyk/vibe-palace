// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

import (
	"fmt"
	"go/ast"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// hermeticTestMain pins that every test package runs hermetically.
//
// # Why this rule exists
//
// testutil.RunHermetic points the process at a read-only host-config fixture
// and makes git hermetic: one fixed test identity through GIT_CONFIG_GLOBAL, no
// system config, no ambient GIT_AUTHOR_*/GIT_COMMITTER_*. A package whose
// TestMain does not call it runs on the developer's own git config, so a test
// that commits passes on a host with a ~/.gitconfig identity and fails on a CI
// runner with none (TestConfigSync_InSync, red on CI and green on every
// developer host). The difference is invisible locally, so it is pinned here
// rather than left for CI to find.
//
// It reports every directory holding a _test.go file in which no test file
// declares a TestMain that calls RunHermetic: testutil.RunHermetic through the
// file's import of internal/testutil (any alias), or RunHermetic unqualified
// inside package testutil. internal/testutil itself is exempt: it is the
// package that defines RunHermetic, and its own tests exercise the pieces
// directly. There is no baseline: a new test package adds the one-line
// TestMain.
func hermeticTestMain(files []file) []Finding {
	type dirState struct {
		hasTest, hermetic, isTestutil bool
		pos                           string
	}
	dirs := map[string]*dirState{}
	for _, f := range files {
		if !f.isTest {
			continue
		}
		dir := filepath.ToSlash(filepath.Dir(f.path))
		st := dirs[dir]
		if st == nil {
			st = &dirState{pos: f.path}
			dirs[dir] = st
		}
		st.hasTest = true
		if strings.TrimSuffix(f.ast.Name.Name, "_test") == "testutil" && filepath.Base(dir) == "testutil" {
			st.isTestutil = true
		}
		if hasHermeticTestMain(f) {
			st.hermetic = true
		}
	}
	var keys []string
	for dir, st := range dirs {
		if st.hasTest && !st.hermetic && !st.isTestutil {
			keys = append(keys, dir)
		}
	}
	sort.Strings(keys)
	var out []Finding
	for _, dir := range keys {
		rel := dir
		for strings.HasPrefix(rel, "../") || strings.HasPrefix(rel, "./") {
			rel = rel[strings.Index(rel, "/")+1:]
		}
		out = append(out, Finding{
			Kind:   KindHermeticTestMain,
			Symbol: rel,
			Pos:    dirs[dir].pos,
			Detail: fmt.Sprintf("test package %s has no TestMain calling testutil.RunHermetic, so its tests run on the "+
				"host's git config and identity: a test that commits passes on a developer host and fails on a CI "+
				"runner with no git identity. Add `func TestMain(m *testing.M) { os.Exit(testutil.RunHermetic(m)) }` "+
				"(or call RunHermetic from the package's existing TestMain).", rel),
		})
	}
	return out
}

// hasHermeticTestMain reports whether f declares a TestMain whose body calls
// RunHermetic.
func hasHermeticTestMain(f file) bool {
	testutilNames := map[string]bool{}
	for _, im := range f.ast.Imports {
		p, _ := strconv.Unquote(im.Path.Value)
		if p == "testutil" || strings.HasSuffix(p, "/internal/testutil") {
			name := "testutil"
			if im.Name != nil {
				name = im.Name.Name
			}
			testutilNames[name] = true
		}
	}
	inTestutil := f.ast.Name.Name == "testutil"
	for _, d := range f.ast.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Name.Name != "TestMain" || fn.Body == nil {
			continue
		}
		found := false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fun := call.Fun.(type) {
			case *ast.SelectorExpr:
				if id, ok := fun.X.(*ast.Ident); ok && testutilNames[id.Name] && fun.Sel.Name == "RunHermetic" {
					found = true
				}
			case *ast.Ident:
				if (inTestutil || testutilNames["."]) && fun.Name == "RunHermetic" {
					found = true
				}
			}
			return !found
		})
		if found {
			return true
		}
	}
	return false
}
