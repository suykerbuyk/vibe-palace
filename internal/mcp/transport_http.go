// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
)

// HTTPServer exposes the tool registry over HTTP REST endpoints.
type HTTPServer struct {
	srv     *Server
	addr    string
	httpSrv *http.Server
}

// NewHTTPServer creates an HTTP transport for the given MCP server.
// The addr parameter is a host:port string (e.g. ":7423").
func NewHTTPServer(srv *Server, addr string) *HTTPServer {
	h := &HTTPServer{srv: srv, addr: addr}
	h.httpSrv = &http.Server{
		Addr:    addr,
		Handler: h.routes(),
	}
	return h
}

// Handler returns the http.Handler for use in tests.
func (h *HTTPServer) Handler() http.Handler {
	return h.httpSrv.Handler
}

// ListenAndServe starts the HTTP server. It blocks until the context is
// cancelled or the server encounters a fatal error.
func (h *HTTPServer) ListenAndServe(ctx context.Context) error {
	ln, err := net.Listen("tcp", h.addr)
	if err != nil {
		return err
	}

	go func() {
		<-ctx.Done()
		h.httpSrv.Shutdown(context.Background())
	}()

	err = h.httpSrv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown gracefully shuts down the HTTP server.
func (h *HTTPServer) Shutdown(ctx context.Context) error {
	return h.httpSrv.Shutdown(ctx)
}

// routes builds the HTTP mux with CORS middleware applied.
func (h *HTTPServer) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", h.handleHealth)
	mux.HandleFunc("GET /tools", h.handleListTools)
	mux.HandleFunc("POST /tools/{name}", h.handleCallTool)
	return corsMiddleware(mux)
}

// handleHealth returns server status and version.
func (h *HTTPServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"version": "0.1.0",
	})
}

// handleListTools returns all registered tools with their schemas.
func (h *HTTPServer) handleListTools(w http.ResponseWriter, r *http.Request) {
	tools := h.srv.Registry().List()
	writeJSON(w, http.StatusOK, tools)
}

// handleCallTool dispatches a tool invocation with the request body as params.
func (h *HTTPServer) handleCallTool(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("failed to read request body"))
		return
	}

	// Default to empty object if no body provided.
	params := json.RawMessage(body)
	if len(params) == 0 {
		params = json.RawMessage(`{}`)
	}

	// Validate that the body is valid JSON.
	if !json.Valid(params) {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid JSON in request body"))
		return
	}

	ctx := h.srv.contextFunc(r.Context())
	result, err := h.srv.Registry().Dispatch(ctx, name, params)
	if err != nil {
		var notFound *ToolNotFoundError
		var valErr *ValidationError
		switch {
		case errors.As(err, &notFound):
			writeJSON(w, http.StatusNotFound, errorBody(err.Error()))
		case errors.As(err, &valErr):
			writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		default:
			writeJSON(w, http.StatusInternalServerError, errorBody(err.Error()))
		}
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"result": result})
}

// corsMiddleware adds CORS headers for local browser-based clients.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// writeJSON marshals v as JSON and writes it with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// errorBody returns a standard error response envelope.
func errorBody(msg string) map[string]string {
	return map[string]string{"error": msg}
}
