// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// tomleq decodes two TOML files into map[string]any and compares them by
// reflect.DeepEqual. Exit 0 on equal, 1 on differ, 2 on usage / IO errors.
package main

import (
	"fmt"
	"os"
	"reflect"

	"github.com/BurntSushi/toml"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: tomleq <fileA> <fileB>")
		os.Exit(2)
	}
	a, err := decode(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "tomleq: %v\n", err)
		os.Exit(2)
	}
	b, err := decode(os.Args[2])
	if err != nil {
		fmt.Fprintf(os.Stderr, "tomleq: %v\n", err)
		os.Exit(2)
	}
	if reflect.DeepEqual(a, b) {
		os.Exit(0)
	}
	fmt.Fprintf(os.Stderr, "--- %s\n%#v\n+++ %s\n%#v\n", os.Args[1], a, os.Args[2], b)
	os.Exit(1)
}

func decode(path string) (map[string]any, error) {
	var m map[string]any
	if _, err := toml.DecodeFile(path, &m); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	return m, nil
}
