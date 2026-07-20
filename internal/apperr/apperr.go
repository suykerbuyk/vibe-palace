// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package apperr carries the caller-vs-fault taxonomy that keeps vp_health
// honest.
//
// It is a LEAF package on purpose: it imports NOTHING internal, so both the
// tool/mcp layer (internal/mcp, internal/tools) and the layer beneath it
// (internal/vaultfs, internal/storage) can mark their errors with it without an
// import cycle. internal/mcp imports storage one-way; a caller-error type living
// in either of those packages could not be used by the other. This is the same
// cycle trap that reshaped search-index-eviction — routed around by putting the
// shared sentinel where neither side has to import the other.
package apperr

import "errors"

// CallerError marks an error as the CALLER's fault — a bad parameter, a failed
// precondition, a compare-and-set conflict, a cap exceeded, a guard correctly
// rejecting bad input — as distinct from an internal fault (I/O failure, corrupt
// state, a bug).
//
// The distinction is who is at fault, and it is the whole point: a tool that did
// its job by rejecting bad input must not be logged, counted, or surfaced as a
// system-health problem. makeHandler discovers this via errors.As and stamps the
// log line fault="caller", which vplog.Summarize excludes from health status
// while still counting it as caller friction.
//
// It is a VALUE type wrapping an underlying error, discoverable with
// errors.As(err, &CallerError{}) and transparent to errors.Is/Unwrap. Wrap a
// known caller error at its source and let it propagate; anything left unwrapped
// defaults to internal (amber), so a partial classification stays honest.
type CallerError struct {
	Err error
}

// Error returns the wrapped error's message unchanged: the classification is
// carried by the type, not by decorating the text.
func (e CallerError) Error() string {
	if e.Err == nil {
		return "caller error"
	}
	return e.Err.Error()
}

// Unwrap exposes the underlying error so errors.Is/As continue down the chain.
func (e CallerError) Unwrap() error { return e.Err }

// Caller wraps err as a CallerError. It returns nil for a nil err so it is safe
// to apply unconditionally at a return site. The result is an error whose type
// is discoverable with errors.As(err, &CallerError{}) even after further %w
// wrapping upstream.
func Caller(err error) error {
	if err == nil {
		return nil
	}
	return CallerError{Err: err}
}

// IsCaller reports whether err is, or wraps, a CallerError.
func IsCaller(err error) bool {
	var ce CallerError
	return errors.As(err, &ce)
}
