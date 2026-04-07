// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// EmbedCache manages on-disk embedding vector cache files at
// palace/{project}/.local/embed-cache/{drawerID}.vec.
// Each file stores raw little-endian float32 values.
type EmbedCache struct {
	vault *storage.Vault
}

// NewEmbedCache creates a new embedding cache backed by the vault.
func NewEmbedCache(vault *storage.Vault) *EmbedCache {
	return &EmbedCache{vault: vault}
}

// Get returns a cached embedding vector, or nil if not cached.
func (c *EmbedCache) Get(project, drawerID string) ([]float32, error) {
	path, err := c.path(project, drawerID)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read embed cache: %w", err)
	}

	if len(data)%4 != 0 {
		return nil, fmt.Errorf("corrupt cache file %s: size %d not divisible by 4", path, len(data))
	}

	vec := make([]float32, len(data)/4)
	for i := range vec {
		bits := binary.LittleEndian.Uint32(data[i*4:])
		vec[i] = math.Float32frombits(bits)
	}
	return vec, nil
}

// Put stores an embedding vector in the cache.
func (c *EmbedCache) Put(project, drawerID string, vec []float32) error {
	path, err := c.path(project, drawerID)
	if err != nil {
		return err
	}

	if err := storage.EnsureDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("ensure embed cache dir: %w", err)
	}

	data := make([]byte, len(vec)*4)
	for i, v := range vec {
		binary.LittleEndian.PutUint32(data[i*4:], math.Float32bits(v))
	}

	return os.WriteFile(path, data, 0644)
}

// path returns palace/{project}/.local/embed-cache/{drawerID}.vec.
func (c *EmbedCache) path(project, drawerID string) (string, error) {
	localDir, err := c.vault.LocalDir(project)
	if err != nil {
		return "", err
	}
	return filepath.Join(localDir, "embed-cache", drawerID+".vec"), nil
}
