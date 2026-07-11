// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package migrate

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/slug"
)

// ErrResolverAbort is returned by a SlugResolver when the user aborts
// the resolution interactively (EOF on stdin, SIGINT, etc.). Callers
// should treat this as a user-facing exit, not a system failure, and
// must not perform any writes.
var ErrResolverAbort = errors.New("slug resolution aborted by user")

// SlugResolver decides how to rename a colliding project directory's
// slug during migration scan. Implementations are called once per
// collision; they must return a slug that does not appear in `taken`
// (the set of slugs already assigned in this scan) and does not match
// any existing `{vault}/Projects/*` directory slug — the caller
// re-checks after Resolve returns.
//
// collidingDir is the later-sorted directory (the one being renamed).
// existingDir is the earlier-sorted directory that already owns the
// slug. slug is the colliding slug value both map to.
type SlugResolver interface {
	Resolve(collidingDir, existingDir, slugVal string, taken map[string]bool) (newSlug string, err error)
}

// proposeDefault returns the first slug-safe escalation of base that is
// not present in taken and not present in onDisk. Format: base_vp,
// base_vp2, base_vp3, ... The returned slug is guaranteed to pass
// slug.Validate().
func proposeDefault(base string, taken, onDisk map[string]bool) string {
	candidate := base + "-vp"
	i := 2
	for taken[candidate] || onDisk[candidate] {
		candidate = fmt.Sprintf("%s-vp%d", base, i)
		i++
	}
	return candidate
}

// scanOnDiskSlugs returns the set of slugs that correspond to existing
// directories under {vaultRoot}/Projects/. It is used to avoid
// proposing rename targets that collide with prior migrations' output.
// The slug of a directory is the slugified form of its name — the same
// transformation used on input. If vaultProjectsDir does not exist,
// returns an empty map and nil error.
func scanOnDiskSlugs(vaultProjectsDir string) (map[string]bool, error) {
	out := make(map[string]bool)
	entries, err := os.ReadDir(vaultProjectsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		s := slug.Slugify(e.Name())
		if s != "" {
			out[s] = true
		}
	}
	return out, nil
}

// AutoResolver accepts the default rename suggestion (base_vp, escalating
// to base_vp2, base_vp3, ...) without prompting. It never reads from
// stdin and never errors. Each adoption is logged via slog.Warn so the
// user-visible rename decision leaves an audit trail per the
// capture-and-log discipline (no silent user-facing decisions).
type AutoResolver struct {
	// OnDisk is the set of slugs already present as directories under
	// {vault}/Projects/ (populated by the caller). AutoResolver will
	// not propose any slug in this set.
	OnDisk map[string]bool
}

// Resolve returns the first base_vpN that is unused.
func (r *AutoResolver) Resolve(collidingDir, existingDir, slugVal string, taken map[string]bool) (string, error) {
	proposed := proposeDefault(slugVal, taken, r.OnDisk)
	slog.Warn("migrate.AutoResolver: auto-accepting default rename",
		"op", "migrate.AutoResolver.Resolve",
		"slug", slugVal,
		"proposed", proposed,
		"colliding_dir", collidingDir,
		"existing_dir", existingDir,
	)
	return proposed, nil
}

// MapResolver consumes a pre-specified slug map (from --slug-map).
// Keys are the original colliding slug; values are the desired
// replacement. On a miss, Resolve falls through to Fallback (which
// may be nil — in which case Resolve returns an error).
type MapResolver struct {
	Map      map[string]string
	Fallback SlugResolver
}

// Resolve looks up slugVal in the map; on hit validates and returns.
// On miss delegates to Fallback.
func (r *MapResolver) Resolve(collidingDir, existingDir, slugVal string, taken map[string]bool) (string, error) {
	if mapped, ok := r.Map[slugVal]; ok {
		if err := slug.Validate(mapped); err != nil {
			return "", fmt.Errorf("slug-map value for %q is invalid: %w", slugVal, err)
		}
		if taken[mapped] {
			return "", fmt.Errorf("slug-map target %q is already taken by another project in this scan", mapped)
		}
		return mapped, nil
	}
	if r.Fallback == nil {
		return "", fmt.Errorf("slug-map has no entry for %q and no fallback resolver is configured", slugVal)
	}
	return r.Fallback.Resolve(collidingDir, existingDir, slugVal, taken)
}

// InteractiveResolver prompts the user on stdin/stdout for a rename.
// It proposes a default (base_vp escalation); the user can accept with
// Enter, supply a custom slug, or send EOF / Ctrl-D to abort.
type InteractiveResolver struct {
	In     io.Reader // defaults to os.Stdin
	Out    io.Writer // defaults to os.Stderr
	OnDisk map[string]bool
}

// Resolve prompts interactively and loops until the user provides a
// valid, non-taken, non-on-disk slug. Returns ErrResolverAbort on
// stdin EOF (no input available).
func (r *InteractiveResolver) Resolve(collidingDir, existingDir, slugVal string, taken map[string]bool) (string, error) {
	in := r.In
	if in == nil {
		in = os.Stdin
	}
	out := r.Out
	if out == nil {
		out = os.Stderr
	}

	reader := bufio.NewReader(in)
	fmt.Fprintf(out, "\nSlug collision detected:\n")
	fmt.Fprintf(out, "  existing: %s\n", existingDir)
	fmt.Fprintf(out, "  colliding: %s\n", collidingDir)
	fmt.Fprintf(out, "  both map to slug: %q\n", slugVal)

	for {
		proposal := proposeDefault(slugVal, taken, r.OnDisk)
		fmt.Fprintf(out, "Rename %q to [%s]: ", filepath.Base(collidingDir), proposal)

		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF && line == "" {
				return "", ErrResolverAbort
			}
			if err != io.EOF {
				return "", ErrResolverAbort
			}
			// EOF with partial line — treat the partial line as input.
		}
		entered := strings.TrimSpace(line)
		if entered == "" {
			entered = proposal
		}
		if verr := slug.Validate(entered); verr != nil {
			fmt.Fprintf(out, "  invalid slug: %v\n", verr)
			continue
		}
		if taken[entered] {
			fmt.Fprintf(out, "  %q is already used by another project in this scan; try again\n", entered)
			continue
		}
		if r.OnDisk[entered] {
			fmt.Fprintf(out, "  %q already exists as a directory in the vault; try again\n", entered)
			continue
		}
		return entered, nil
	}
}

// ParseSlugMap parses a --slug-map flag value into a map.
// Format: "OLD=NEW,OLD2=NEW2". Whitespace around entries is trimmed.
// An empty string returns an empty (non-nil) map.
func ParseSlugMap(s string) (map[string]string, error) {
	out := make(map[string]string)
	if strings.TrimSpace(s) == "" {
		return out, nil
	}
	for pair := range strings.SplitSeq(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		before, after, ok := strings.Cut(pair, "=")
		if !ok {
			return nil, fmt.Errorf("slug-map entry %q missing '='", pair)
		}
		k := strings.TrimSpace(before)
		v := strings.TrimSpace(after)
		if k == "" || v == "" {
			return nil, fmt.Errorf("slug-map entry %q has empty key or value", pair)
		}
		if _, dup := out[k]; dup {
			return nil, fmt.Errorf("slug-map has duplicate key %q", k)
		}
		if err := slug.Validate(v); err != nil {
			return nil, fmt.Errorf("slug-map value %q invalid: %w", v, err)
		}
		out[k] = v
	}
	return out, nil
}
