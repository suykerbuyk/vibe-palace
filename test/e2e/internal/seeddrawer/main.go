// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// seeddrawer appends a single storage.Drawer to the vault for e2e rigs.
//
// Usage: seeddrawer <project> <room> <content>
//
// Note: the third argv is the ROOM slug (disk layout is
// drawers/{wing}/{room}/drawers.jsonl). Hall is a classifier tag on the
// drawer record; we leave it empty here — AppendDrawer does not require it.
// Room is passed explicitly because AppendDrawer needs a target room path.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/palace"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: seeddrawer <project> <room> <content>")
		os.Exit(2)
	}
	project, room, content := os.Args[1], os.Args[2], os.Args[3]

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "seeddrawer: resolve home: %v\n", err)
		os.Exit(1)
	}
	vault := storage.NewVault(filepath.Join(home, "vibe-palace-vault"))
	wing := palace.DetectWing(project, "")

	d := storage.Drawer{
		Hall:       "",
		Content:    content,
		SourceType: "seed",
		AddedBy:    "e2e-rig",
		FiledAt:    time.Now().UTC().Format(time.RFC3339),
	}
	if err := vault.AppendDrawer(project, wing, room, d); err != nil {
		fmt.Fprintf(os.Stderr, "seeddrawer: append: %v\n", err)
		os.Exit(1)
	}
	path, err := vault.DrawerFile(project, wing, room)
	if err != nil {
		fmt.Fprintf(os.Stderr, "seeddrawer: drawer path: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("%s %s\n", storage.DrawerID(wing, content), path)
}
