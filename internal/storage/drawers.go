// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"bufio"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Drawer represents a verbatim content chunk stored in the palace.
type Drawer struct {
	ID         string `json:"id"`
	Hall       string `json:"hall"`
	Content    string `json:"content"`
	SourceType string `json:"source_type"`
	SourceRef  string `json:"source_ref,omitempty"`
	ChunkIndex int    `json:"chunk_index,omitempty"`
	FiledAt    string `json:"filed_at"`
	AddedBy    string `json:"added_by,omitempty"`
}

// drawerID generates a deterministic ID: first 8 hex chars of md5(wing+room+content).
func drawerID(wing, room, content string) string {
	h := md5.Sum([]byte(wing + room + content))
	return hex.EncodeToString(h[:])[:8]
}

// AppendDrawer appends a drawer to the JSONL file for the given room.
// It generates a deterministic ID and rejects duplicates.
func (v *Vault) AppendDrawer(project, wing, room string, d Drawer) error {
	if err := validateSlugs(project, wing, room); err != nil {
		return err
	}

	d.ID = drawerID(wing, room, d.Content)

	path, err := v.DrawerFile(project, wing, room)
	if err != nil {
		return err
	}
	if err := EnsureDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("ensure drawer dir: %w", err)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("open drawer file: %w", err)
	}
	defer f.Close()

	if err := flockFile(f); err != nil {
		return fmt.Errorf("lock drawer file: %w", err)
	}
	defer funlockFile(f)

	// Check for duplicate ID.
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var existing Drawer
		if err := json.Unmarshal(scanner.Bytes(), &existing); err != nil {
			continue
		}
		if existing.ID == d.ID {
			return fmt.Errorf("drawer %q already exists in %s/%s", d.ID, wing, room)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan drawer file: %w", err)
	}

	line, err := json.Marshal(d)
	if err != nil {
		return fmt.Errorf("marshal drawer: %w", err)
	}
	line = append(line, '\n')

	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("write drawer: %w", err)
	}
	return nil
}

// GetDrawer retrieves a single drawer by ID from the given room.
func (v *Vault) GetDrawer(project, wing, room, id string) (Drawer, error) {
	drawers, err := v.readDrawerFile(project, wing, room)
	if err != nil {
		return Drawer{}, err
	}
	for _, d := range drawers {
		if d.ID == id {
			return d, nil
		}
	}
	return Drawer{}, fmt.Errorf("drawer %q not found in %s/%s", id, wing, room)
}

// ListDrawers returns all drawers in a room.
func (v *Vault) ListDrawers(project, wing, room string) ([]Drawer, error) {
	return v.readDrawerFile(project, wing, room)
}

// ListDrawersByWing returns all drawers across all rooms in a wing.
func (v *Vault) ListDrawersByWing(project, wing string) ([]Drawer, error) {
	if err := validateSlugs(project, wing); err != nil {
		return nil, err
	}

	wingDir := filepath.Join(v.Root, "palace", project, "drawers", wing)
	entries, err := os.ReadDir(wingDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read wing dir: %w", err)
	}

	var all []Drawer
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		room := e.Name()
		drawers, err := v.readDrawerFile(project, wing, room)
		if err != nil {
			return nil, err
		}
		all = append(all, drawers...)
	}
	return all, nil
}

// DeleteDrawer removes a drawer by ID, rewriting the JSONL file without it.
func (v *Vault) DeleteDrawer(project, wing, room, id string) error {
	path, err := v.DrawerFile(project, wing, room)
	if err != nil {
		return err
	}

	f, err := os.OpenFile(path, os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("open drawer file: %w", err)
	}
	defer f.Close()

	if err := flockFile(f); err != nil {
		return fmt.Errorf("lock drawer file: %w", err)
	}
	defer funlockFile(f)

	var kept [][]byte
	found := false
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		var d Drawer
		if err := json.Unmarshal(line, &d); err == nil && d.ID == id {
			found = true
			continue
		}
		cp := make([]byte, len(line))
		copy(cp, line)
		kept = append(kept, cp)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan drawer file: %w", err)
	}
	if !found {
		return fmt.Errorf("drawer %q not found in %s/%s", id, wing, room)
	}

	if err := f.Truncate(0); err != nil {
		return fmt.Errorf("truncate drawer file: %w", err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		return fmt.Errorf("seek drawer file: %w", err)
	}
	for _, line := range kept {
		if _, err := f.Write(append(line, '\n')); err != nil {
			return fmt.Errorf("rewrite drawer file: %w", err)
		}
	}
	return nil
}

// DrawerExists reports whether a drawer with the given ID exists in the room.
func (v *Vault) DrawerExists(project, wing, room, id string) (bool, error) {
	path, err := v.DrawerFile(project, wing, room)
	if err != nil {
		return false, err
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("open drawer file: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var d Drawer
		if err := json.Unmarshal(scanner.Bytes(), &d); err != nil {
			continue
		}
		if d.ID == id {
			return true, nil
		}
	}
	return false, scanner.Err()
}

// ListWings returns all wing slugs for a project by reading the drawers directory.
func (v *Vault) ListWings(project string) ([]string, error) {
	if err := ValidateSlug(project); err != nil {
		return nil, fmt.Errorf("project: %w", err)
	}

	drawersDir := filepath.Join(v.Root, "palace", project, "drawers")
	entries, err := os.ReadDir(drawersDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read drawers dir: %w", err)
	}

	var wings []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if ValidateSlug(e.Name()) == nil {
			wings = append(wings, e.Name())
		}
	}
	return wings, nil
}

// ListRooms returns all room slugs for a wing by reading the wing directory.
func (v *Vault) ListRooms(project, wing string) ([]string, error) {
	if err := validateSlugs(project, wing); err != nil {
		return nil, err
	}

	wingDir := filepath.Join(v.Root, "palace", project, "drawers", wing)
	entries, err := os.ReadDir(wingDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read wing dir: %w", err)
	}

	var rooms []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if ValidateSlug(e.Name()) == nil {
			rooms = append(rooms, e.Name())
		}
	}
	return rooms, nil
}

// ListProjects returns all project slugs by reading the palace directory.
func (v *Vault) ListProjects() ([]string, error) {
	palaceDir := filepath.Join(v.Root, "palace")
	entries, err := os.ReadDir(palaceDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read palace dir: %w", err)
	}

	var projects []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if name == ".local" {
			continue
		}
		if ValidateSlug(name) == nil {
			projects = append(projects, name)
		}
	}
	return projects, nil
}

// readDrawerFile reads and parses all drawers from a room's JSONL file.
func (v *Vault) readDrawerFile(project, wing, room string) ([]Drawer, error) {
	path, err := v.DrawerFile(project, wing, room)
	if err != nil {
		return nil, err
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open drawer file: %w", err)
	}
	defer f.Close()

	var drawers []Drawer
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var d Drawer
		if err := json.Unmarshal(scanner.Bytes(), &d); err != nil {
			return nil, fmt.Errorf("parse drawer line: %w", err)
		}
		drawers = append(drawers, d)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan drawer file: %w", err)
	}
	return drawers, nil
}
