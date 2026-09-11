package cli

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
)

func TestPromptChoice(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"accept", "a\n", "a"},
		{"accept-all", "A\n", "A"},
		{"skip", "s\n", "s"},
		{"quit", "q\n", "q"},
		{"whitespace-trimmed", "  a  \n", "a"},
		{"retry then accept", "wat\n?\na\n", "a"},
		{"eof with no input becomes skip", "", "s"},
		{"unknown hits bound becomes skip", "x\ny\nz\n1\n2\n", "s"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var w bytes.Buffer
			r := bufio.NewReader(strings.NewReader(tc.input))
			got, err := PromptChoice(&w, r)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q (output=%q)", got, tc.want, w.String())
			}
		})
	}
}

func TestPromptTemplateChoice(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"skip", "s\n", "s"},
		{"new", "n\n", "n"},
		{"skip-all", "S\n", "S"},
		{"new-all", "N\n", "N"},
		{"quit", "q\n", "q"},
		{"eof becomes skip", "", "s"},
		{"retry then new", "x\n?\nn\n", "n"},
		{"retry-exhausted becomes skip", "x\ny\nz\n1\n2\n", "s"},
		// There is no overwrite answer: o and O re-prompt like any other
		// unrecognized input, and EOF after them is still the keep answer.
		{"o re-prompts then skip", "o\ns\n", "s"},
		{"O re-prompts then new", "O\nn\n", "n"},
		{"o then eof becomes skip", "o\n", "s"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var w bytes.Buffer
			r := bufio.NewReader(strings.NewReader(tc.input))
			got, err := PromptTemplateChoice(&w, r)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q (output=%q)", got, tc.want, w.String())
			}
		})
	}
}

// TestPromptTemplateChoiceMenu pins the menu the operator reads: keep and
// sidecar only, and no word of an overwrite. `o` rejected with the menu
// re-shown is what an operator with the old answer in muscle memory sees.
func TestPromptTemplateChoiceMenu(t *testing.T) {
	var w bytes.Buffer
	got, err := PromptTemplateChoice(&w, bufio.NewReader(strings.NewReader("o\ns\n")))
	if err != nil || got != "s" {
		t.Fatalf("got %q, %v", got, err)
	}
	out := w.String()
	const menu = "[s]kip — keep your file / [n]ew-sidecar — write <file>.new with the embedded copy — uppercase for all remaining items, [q]uit: "
	if strings.Count(out, menu) != 2 {
		t.Errorf("menu should be shown twice (o rejected, then s):\n%s", out)
	}
	if !strings.Contains(out, "Please answer s, n, S, N, or q.") {
		t.Errorf("o was not rejected:\n%s", out)
	}
	if strings.Contains(strings.ToLower(out), "overwrite") {
		t.Errorf("the menu still offers an overwrite:\n%s", out)
	}
}
