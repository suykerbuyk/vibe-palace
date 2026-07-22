// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// TestFlexStringListUnmarshal covers the coercion in isolation: an array stays an
// array, a lone string becomes a one-element list, and empty/null become an empty
// list. This is the Go decode half of the fix for Grok's capture (which sends the
// list fields as strings).
func TestFlexStringListUnmarshal(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want flexStringList
	}{
		{"array", `["a","b"]`, flexStringList{"a", "b"}},
		{"empty array", `[]`, flexStringList{}},
		{"single string", `"one decision"`, flexStringList{"one decision"}},
		{"empty string", `""`, nil},
		{"null", `null`, nil},
		{"whitespace-padded string", `  "padded"  `, flexStringList{"padded"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got flexStringList
			if err := json.Unmarshal([]byte(tc.in), &got); err != nil {
				t.Fatalf("Unmarshal(%s): %v", tc.in, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Unmarshal(%s) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}

// TestCaptureSessionParamsCoercesStringListFields proves the struct wires the
// custom unmarshaler through: a Grok-shaped payload with string-valued list fields
// decodes to one-element lists rather than failing.
func TestCaptureSessionParamsCoercesStringListFields(t *testing.T) {
	payload := `{
		"project": "p",
		"summary": "s",
		"decisions": "single decision",
		"files_changed": "one/file.go",
		"open_threads": "one thread"
	}`
	var p captureSessionParams
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(p.Decisions, flexStringList{"single decision"}) {
		t.Errorf("Decisions = %#v", p.Decisions)
	}
	if !reflect.DeepEqual(p.FilesChanged, flexStringList{"one/file.go"}) {
		t.Errorf("FilesChanged = %#v", p.FilesChanged)
	}
	if !reflect.DeepEqual(p.OpenThreads, flexStringList{"one thread"}) {
		t.Errorf("OpenThreads = %#v", p.OpenThreads)
	}
}

// TestCaptureSessionSchemaAcceptsStringListFields is the regression guard for the
// SCHEMA half — the layer that actually rejected Grok's capture at
// mcp.makeHandler ("got string, want array"). It compiles captureSessionSchema
// with the same validator the MCP layer uses and asserts string-valued list
// fields now validate, while arrays still validate and a wrong type still fails.
func TestCaptureSessionSchemaAcceptsStringListFields(t *testing.T) {
	c := jsonschema.NewCompiler()
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(string(captureSessionSchema)))
	if err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	const url = "vibe-palace://test/capture-session/schema.json"
	if err := c.AddResource(url, doc); err != nil {
		t.Fatalf("add resource: %v", err)
	}
	compiled, err := c.Compile(url)
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}

	validate := func(params string) error {
		d, err := jsonschema.UnmarshalJSON(strings.NewReader(params))
		if err != nil {
			return err
		}
		return compiled.Validate(d)
	}

	// The Grok shape: list fields as strings — must now pass.
	if err := validate(`{"project":"p","summary":"s","decisions":"d","open_threads":"o","files_changed":"f"}`); err != nil {
		t.Errorf("string-valued list fields should validate, got: %v", err)
	}
	// The original shape: arrays — must still pass.
	if err := validate(`{"project":"p","summary":"s","decisions":["d1","d2"],"files_changed":["f"]}`); err != nil {
		t.Errorf("array-valued list fields should validate, got: %v", err)
	}
	// A genuinely wrong type is still rejected.
	if err := validate(`{"project":"p","summary":"s","decisions":123}`); err == nil {
		t.Error("a number for decisions should still fail validation")
	}
}

// TestCaptureSessionHandlerAcceptsStringListFields runs the handler end to end
// with the Grok-shaped string list fields to confirm the capture succeeds rather
// than being lost.
func TestCaptureSessionHandlerAcceptsStringListFields(t *testing.T) {
	vault := testSessionVault(t)
	tool := CaptureSessionTool(vault, nil)

	params := json.RawMessage(`{
		"project": "test-proj",
		"summary": "Grok-shaped capture with string list fields.",
		"decisions": "Coerce string to one-element list",
		"open_threads": "Verify against the live path"
	}`)

	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	r, ok := result.(captureSessionResult)
	if !ok {
		t.Fatalf("unexpected result type: %T", result)
	}
	if r.Status != "ok" {
		t.Errorf("status = %q, want ok", r.Status)
	}
}
