// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package llm

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// retryWithBackoff runs fn with exponential backoff, retrying on transport
// errors and retryable HTTP statuses (429 and 5xx). It performs up to
// maxRetries retries (maxRetries+1 attempts total).
//
// On a transport error it returns ctx.Err() if the context is done, otherwise
// it retries; once retries are exhausted it wraps the error with the
// "request failed after N retries" message.
//
// On a retryable status it closes the response body before looping. When
// retries are exhausted it returns the LAST response (body unread) so the
// caller can read the body for its error message. Non-retryable statuses are
// returned immediately for the caller to handle.
func retryWithBackoff(ctx context.Context, initialBackoff time.Duration, fn func() (*http.Response, error)) (*http.Response, error) {
	backoff := initialBackoff

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			slog.Warn("llm: retrying", "attempt", attempt, "backoff", backoff)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
			backoff *= 4
		}

		resp, err := fn()
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			slog.Warn("llm: request failed", "err", err, "attempt", attempt)
			if attempt < maxRetries {
				continue
			}
			return nil, fmt.Errorf("llm: request failed after %d retries: %w", maxRetries, err)
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			slog.Warn("llm: retryable status", "status", resp.StatusCode, "attempt", attempt)
			if attempt < maxRetries {
				resp.Body.Close()
				continue
			}
			return resp, nil
		}

		return resp, nil
	}

	// Should not reach here, but satisfy the compiler.
	return nil, fmt.Errorf("llm: exhausted retries")
}
