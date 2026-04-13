package cli

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// PromptChoice reads a single-line accept/skip/accept-all/quit response
// from r, writing the prompt to w. It returns one of "a", "A", "s", or "q".
// Unrecognized input repeats the prompt up to a small bound; EOF returns "s".
func PromptChoice(w io.Writer, r *bufio.Reader) (string, error) {
	for range 5 {
		fmt.Fprint(w, "Accept this change? [a]ccept / [s]kip / [A]ccept-all / [q]uit: ")
		line, err := r.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", err
		}
		line = strings.TrimSpace(line)
		switch line {
		case "a", "A", "s", "q":
			return line, nil
		}
		fmt.Fprintln(w, "Please answer a, s, A, or q.")
		if err == io.EOF {
			return "s", nil
		}
	}
	return "s", nil
}
