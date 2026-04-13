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
