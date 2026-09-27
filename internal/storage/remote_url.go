// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"net/url"
	"strings"
)

// normaliseRemoteURL reduces a git remote URL to "host/path", so the spellings
// of ONE repository compare equal:
//
//	ssh://git@gitlab.example.com:22/team/vault.git
//	git@gitlab.example.com:team/vault.git          (scp-like)
//	https://gitlab.example.com/team/vault
//
// all become "gitlab.example.com/team/vault". The scheme, user and port are
// dropped; the HOST is lowercased (DNS is case-insensitive) but the PATH keeps
// its case (it can matter on the server); leading and trailing slashes and one
// trailing ".git" are stripped. file:// URLs normalise to their path.
//
// ok is false for anything that is not a URL of one of those shapes — a
// free-text label, a bare local path — which then matches nothing.
func normaliseRemoteURL(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", false
	}
	var host, path string
	switch {
	case strings.Contains(s, "://"):
		u, err := url.Parse(s)
		if err != nil || u.Path == "" {
			return "", false
		}
		switch strings.ToLower(u.Scheme) {
		case "ssh", "git", "http", "https", "git+ssh", "ssh+git", "file":
		default:
			return "", false
		}
		host, path = strings.ToLower(u.Hostname()), u.Path
		if host == "" && strings.ToLower(u.Scheme) != "file" {
			return "", false
		}
	default:
		// scp-like: [user@]host:path, with no slash before the colon (that
		// would be a local path) and a non-empty host and path.
		colon := strings.Index(s, ":")
		if colon <= 0 || strings.Contains(s[:colon], "/") {
			return "", false
		}
		hostPart := s[:colon]
		if at := strings.LastIndex(hostPart, "@"); at >= 0 {
			hostPart = hostPart[at+1:]
		}
		host, path = strings.ToLower(hostPart), s[colon+1:]
		if host == "" || strings.ContainsAny(host, " \t") {
			return "", false
		}
	}
	path = strings.Trim(path, "/")
	path = strings.TrimSuffix(path, ".git")
	path = strings.TrimRight(path, "/")
	if path == "" {
		return "", false
	}
	return host + "/" + path, true
}
