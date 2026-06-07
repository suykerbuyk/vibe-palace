// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// TaskMeta is the lightweight metadata for task listing.
type TaskMeta struct {
	Slug     string `json:"slug"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Priority string `json:"priority"`
	Done     bool   `json:"done"`
}

// validStatuses defines the allowed task status values.
var validStatuses = map[string]bool{
	"pending":     true,
	"in_progress": true,
	"completed":   true,
	"blocked":     true,
	"cancelled":   true,
	"retired":     true,
}

// CreateTask creates a new task markdown file in the project's tasks directory.
func (v *Vault) CreateTask(project, slug, title, content, priority string) error {
	path, err := v.TaskFile(project, slug)
	if err != nil {
		return err
	}

	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("task %q already exists in project %q", slug, project)
	}

	tasksDir, err := v.TasksDir(project)
	if err != nil {
		return err
	}
	if err := EnsureDir(tasksDir); err != nil {
		return fmt.Errorf("ensure tasks dir: %w", err)
	}

	var buf strings.Builder
	fmt.Fprintf(&buf, "# %s\n\n", title)
	fmt.Fprintf(&buf, "**Status:** pending\n")
	fmt.Fprintf(&buf, "**Priority:** %s\n", priority)
	if content != "" {
		buf.WriteString("\n")
		buf.WriteString(content)
		if !strings.HasSuffix(content, "\n") {
			buf.WriteByte('\n')
		}
	}

	return v.lockedWrite(path, []byte(buf.String()))
}

// GetTask reads a task file and returns its metadata and full content.
// It searches active, done, and cancelled directories.
func (v *Vault) GetTask(project, slug string) (TaskMeta, string, error) {
	if err := validateSlugs(project, slug); err != nil {
		return TaskMeta{}, "", err
	}

	type location struct {
		dir  func(string) (string, error)
		done bool
	}
	locations := []location{
		{v.TasksDir, false},
		{v.TaskDoneDir, true},
		{v.TaskCancelledDir, true},
	}

	for _, loc := range locations {
		dir, err := loc.dir(project)
		if err != nil {
			return TaskMeta{}, "", err
		}
		path := filepath.Join(dir, slug+".md")
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return TaskMeta{}, "", fmt.Errorf("read task: %w", err)
		}

		meta := parseTaskMeta(slug, string(data), loc.done)
		return meta, string(data), nil
	}

	return TaskMeta{}, "", fmt.Errorf("task %q not found in project %q", slug, project)
}

// ListTasks returns metadata for all tasks in a project. If includeDone is
// true, also includes tasks from the done/ and cancelled/ subdirectories.
func (v *Vault) ListTasks(project string, includeDone bool) ([]TaskMeta, error) {
	tasksDir, err := v.TasksDir(project)
	if err != nil {
		return nil, err
	}

	var result []TaskMeta

	// Active tasks.
	active, err := globTaskMeta(tasksDir, false)
	if err != nil {
		return nil, err
	}
	result = append(result, active...)

	if includeDone {
		doneDir, err := v.TaskDoneDir(project)
		if err != nil {
			return nil, err
		}
		done, err := globTaskMeta(doneDir, true)
		if err != nil {
			return nil, err
		}
		result = append(result, done...)

		cancelDir, err := v.TaskCancelledDir(project)
		if err != nil {
			return nil, err
		}
		cancelled, err := globTaskMeta(cancelDir, true)
		if err != nil {
			return nil, err
		}
		result = append(result, cancelled...)
	}

	return result, nil
}

// UpdateTaskStatus updates the status line in a task file.
func (v *Vault) UpdateTaskStatus(project, slug, status string) error {
	if !validStatuses[status] {
		return fmt.Errorf("invalid status %q", status)
	}

	path, err := v.TaskFile(project, slug)
	if err != nil {
		return err
	}

	// Hold the per-path lock across the read→rewrite (RMW): a concurrent
	// status update of the same task must not clobber this one.
	release, err := vaultlock.Acquire(v.Root, path)
	if err != nil {
		return fmt.Errorf("lock task: %w", err)
	}
	defer release()

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read task: %w", err)
	}

	updated := replaceStatusLine(string(data), status)
	return atomicfile.Write(v.Root, path, []byte(updated))
}

// RetireTask moves a task to the done/ directory with status "retired".
func (v *Vault) RetireTask(project, slug string) error {
	return v.moveTask(project, slug, v.TaskDoneDir, "retired")
}

// CancelTask moves a task to the cancelled/ directory with status "cancelled".
func (v *Vault) CancelTask(project, slug string) error {
	return v.moveTask(project, slug, v.TaskCancelledDir, "cancelled")
}

// moveTask updates a task's status and moves it to a destination directory.
func (v *Vault) moveTask(project, slug string, destFn func(string) (string, error), status string) error {
	srcPath, err := v.TaskFile(project, slug)
	if err != nil {
		return err
	}

	if _, err := os.Stat(srcPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("task %q not found (may already be %s)", slug, status)
		}
		return err
	}

	// Update status in file before moving.
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("read task: %w", err)
	}
	updated := replaceStatusLine(string(data), status)

	destDir, err := destFn(project)
	if err != nil {
		return err
	}
	if err := EnsureDir(destDir); err != nil {
		return fmt.Errorf("ensure dest dir: %w", err)
	}

	destPath := filepath.Join(destDir, slug+".md")
	if err := v.lockedWrite(destPath, []byte(updated)); err != nil {
		return fmt.Errorf("write to dest: %w", err)
	}

	return os.Remove(srcPath)
}

// parseTaskMeta extracts metadata from task markdown content.
func parseTaskMeta(slug, content string, done bool) TaskMeta {
	meta := TaskMeta{Slug: slug, Done: done}
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "# ") && meta.Title == "" {
			meta.Title = strings.TrimPrefix(trimmed, "# ")
		}
		if strings.HasPrefix(trimmed, "**Status:**") {
			meta.Status = strings.TrimSpace(strings.TrimPrefix(trimmed, "**Status:**"))
		}
		if strings.HasPrefix(trimmed, "**Priority:**") {
			meta.Priority = strings.TrimSpace(strings.TrimPrefix(trimmed, "**Priority:**"))
		}
	}
	return meta
}

// replaceStatusLine replaces the **Status:** line value in task markdown.
func replaceStatusLine(content, newStatus string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "**Status:**") {
			lines[i] = "**Status:** " + newStatus
			break
		}
	}
	return strings.Join(lines, "\n")
}

// globTaskMeta reads all .md files from a directory and returns their metadata.
func globTaskMeta(dir string, done bool) ([]TaskMeta, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		return nil, fmt.Errorf("glob tasks: %w", err)
	}

	var result []TaskMeta
	for _, m := range matches {
		data, err := os.ReadFile(m)
		if err != nil {
			return nil, fmt.Errorf("read task %s: %w", m, err)
		}
		slug := strings.TrimSuffix(filepath.Base(m), ".md")
		meta := parseTaskMeta(slug, string(data), done)
		result = append(result, meta)
	}
	return result, nil
}
