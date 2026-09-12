package cli

import (
	"bufio"
	"bytes"
	"errors"
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

// TestPromptChoice_EOFWithNoInputAborts covers the case the table above used
// to fold into "skip" — a genuine EOF before any valid answer is entered
// (empty stdin: a pipe closed early, or /dev/null) must abort with
// ErrPromptEOF, not silently resolve to "s". See
// commands-upgrade-treats-dev-null-stdin-as-a-terminal.
func TestPromptChoice_EOFWithNoInputAborts(t *testing.T) {
	var w bytes.Buffer
	r := bufio.NewReader(strings.NewReader(""))
	got, err := PromptChoice(&w, r)
	if !errors.Is(err, ErrPromptEOF) {
		t.Fatalf("err = %v, want ErrPromptEOF", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty string alongside the error", got)
	}
}
