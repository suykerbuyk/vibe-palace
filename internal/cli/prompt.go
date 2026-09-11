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
// "s", "n", "S", "N", or "q":
//
//   - s — skip: keep the operator's file exactly as it is
//   - n — write the embedded copy to <file>.new beside it, for review
//   - S / N — apply the choice to the current item AND every
//     remaining Prompt in the batch
//   - q — quit the reconcile loop
//
// There is deliberately no overwrite answer. `o` ("overwrite, writes
// .bak") existed until 2026-09-10: it was the first step of a chain in
// which the next `vp config sync` pruned the overwritten file and
// committed the deletion to every host. `o` and `O` are now unrecognized
// input and re-prompt like any other.
//
// Unrecognized input repeats the prompt up to five times; EOF returns "s".
func PromptTemplateChoice(w io.Writer, r *bufio.Reader) (string, error) {
	for range 5 {
		fmt.Fprint(w, "[s]kip — keep your file / [n]ew-sidecar — write <file>.new with the embedded copy — uppercase for all remaining items, [q]uit: ")
		line, err := r.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", err
		}
		line = strings.TrimSpace(line)
		switch line {
		case "s", "n", "S", "N", "q":
			return line, nil
		}
		fmt.Fprintln(w, "Please answer s, n, S, N, or q.")
		if err == io.EOF {
			return "s", nil
		}
	}
	return "s", nil
}
