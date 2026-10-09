// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build !ORT

package embedder

import (
	"strings"
	"testing"
	"time"
)

// TestDefaultBuildHasNoORT pins the default build's zero-CGO identity at the
// embedder seam: ORTAvailable is false, so backend=ort can never produce vectors
// here. See session_go.go.
func TestDefaultBuildHasNoORT(t *testing.T) {
	if ORTAvailable {
		t.Fatal("ORTAvailable is true on a build without -tags ORT; the session seam leaked the ORT backend into the default build")
	}
}

// TestNewONNXBackendORTFailsLoudOnDefaultBuild is the B-side of requirement #4:
// asking the default (zero-CGO) binary for the ORT backend returns a clear,
// actionable error and does so promptly — before any model-cache lock or ~90 MB
// download — rather than silently falling back to the Go backend.
func TestNewONNXBackendORTFailsLoudOnDefaultBuild(t *testing.T) {
	start := time.Now()
	emb, err := NewONNXBackend(BackendORT, "sentence-transformers/all-MiniLM-L6-v2", t.TempDir(), 256, 32)
	elapsed := time.Since(start)
	if err == nil {
		if emb != nil {
			emb.Close()
		}
		t.Fatal("NewONNXBackend(ort) on a default build returned no error; it must fail loud")
	}
	if emb != nil {
		t.Fatalf("NewONNXBackend(ort) returned a non-nil embedder alongside its error: %v", err)
	}
	msg := err.Error()
	for _, want := range []string{"ort", "-tags ORT", "build-ort"} {
		if !strings.Contains(msg, want) {
			t.Errorf("fail-loud error %q lacks %q (the operator needs to know how to fix it)", msg, want)
		}
	}
	// It must not have reached the lock/download: the gate is the first thing in
	// NewONNXBackend, so this is near-instant.
	if elapsed > 2*time.Second {
		t.Errorf("NewONNXBackend(ort) took %s; the fail-loud gate must precede the model-cache lock/download", elapsed)
	}
}

// TestNewONNXBackendRejectsUnknownBackend guards the backend allow-list.
func TestNewONNXBackendRejectsUnknownBackend(t *testing.T) {
	_, err := NewONNXBackend("tensorrt", "sentence-transformers/all-MiniLM-L6-v2", t.TempDir(), 256, 32)
	if err == nil || !strings.Contains(err.Error(), "unknown backend") {
		t.Fatalf("NewONNXBackend(\"tensorrt\") error = %v, want an \"unknown backend\" rejection", err)
	}
}

// TestRequestedBackendAppliesEnvOverride proves VP_EMBEDDER_BACKEND overrides
// the configured value and that "" / whitespace normalizes to go.
func TestRequestedBackendAppliesEnvOverride(t *testing.T) {
	t.Setenv(EnvBackend, "")
	if got := RequestedBackend(""); got != BackendGo {
		t.Errorf("RequestedBackend(\"\") = %q, want %q", got, BackendGo)
	}
	if got := RequestedBackend("  ORT "); got != BackendORT {
		t.Errorf("RequestedBackend(configured ORT) = %q, want %q (case/space-insensitive)", got, BackendORT)
	}
	t.Setenv(EnvBackend, "ort")
	if got := RequestedBackend("go"); got != BackendORT {
		t.Errorf("env override: RequestedBackend(go) with %s=ort = %q, want %q", EnvBackend, got, BackendORT)
	}
}

// TestResolveBackendMasksORTOnDefaultBuild is the fingerprint-side guarantee of
// B2: on a default binary ResolveBackend always yields go, even when config or
// env asks for ort, so the embed-cache fingerprint string stays byte-identical
// on every default host no matter how it is configured.
func TestResolveBackendMasksORTOnDefaultBuild(t *testing.T) {
	t.Setenv(EnvBackend, "ort")
	if got := ResolveBackend("ort"); got != BackendGo {
		t.Fatalf("ResolveBackend(ort) on a default build = %q, want %q (masked so the go fingerprint never changes)", got, BackendGo)
	}
	// And the fingerprint computed through it is the unchanged go string.
	want := "vp-embed behaviour=2 model=sentence-transformers/all-MiniLM-L6-v2 max_seq_len=256"
	if got := FingerprintBackend(ResolveBackend("ort"), "sentence-transformers/all-MiniLM-L6-v2", 256); got != want {
		t.Fatalf("default-host fingerprint with backend=ort configured = %q, want the go string %q", got, want)
	}
}
