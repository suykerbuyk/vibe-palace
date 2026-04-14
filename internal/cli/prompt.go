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

// PromptTemplateChoice reads a single-line template-reconcile response
// from r, writing the prompt to w. It returns one of
// "s", "o", "n", "S", "O", "N", or "q":
//
//   - s / o / n — apply the choice to the current item only
//   - S / O / N — apply the choice to the current item AND every
//     remaining Prompt in the batch (orchestrator honors the uppercase
//     set similarly to today's acceptAll flag)
//   - q — quit the reconcile loop
//
// Unrecognized input repeats the prompt up to five times; EOF returns "s".
func PromptTemplateChoice(w io.Writer, r *bufio.Reader) (string, error) {
	for range 5 {
		fmt.Fprint(w, "[s]kip / [o]verwrite (writes .bak) / [n]ew-sidecar — uppercase for all remaining items, [q]uit: ")
		line, err := r.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", err
		}
		line = strings.TrimSpace(line)
		switch line {
		case "s", "o", "n", "S", "O", "N", "q":
			return line, nil
		}
		fmt.Fprintln(w, "Please answer s, o, n, S, O, N, or q.")
		if err == io.EOF {
			return "s", nil
		}
	}
	return "s", nil
}
