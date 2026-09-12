package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrPromptEOF is returned by PromptChoice when stdin hits EOF before a
// valid answer is given — a non-interactive input (a pipe or /dev/null),
// not a real user mashing wrong keys. Callers must treat it as an abort,
// not a skip.
var ErrPromptEOF = errors.New("prompt aborted: stdin closed before an answer was given (non-interactive input?)")

// PromptChoice reads a single-line accept/skip/accept-all/quit response
// from r, writing the prompt to w. It returns one of "a", "A", "s", or "q".
// Unrecognized input repeats the prompt up to a small bound; EOF before any
// valid choice is entered returns ErrPromptEOF.
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
		if err == io.EOF {
			return "", ErrPromptEOF
		}
		fmt.Fprintln(w, "Please answer a, s, A, or q.")
	}
	return "s", nil
}
