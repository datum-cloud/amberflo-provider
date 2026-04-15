/*
Copyright 2026 Datum Technology Inc.

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, version 3.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU Affero General Public License for more details.
*/

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadAPIKey covers the four documented precedence branches of
// loadAPIKey: readable file wins; empty file fails loudly; missing file
// falls back to env var; neither source available is an error.
func TestLoadAPIKey(t *testing.T) {
	dir := t.TempDir()

	writeFile := func(name, contents string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(contents), 0o600); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
		return p
	}

	t.Run("file path is preferred", func(t *testing.T) {
		p := writeFile("ok", "from-file\n")
		key, src, err := loadAPIKey(p, "from-env")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if key != "from-file" {
			t.Fatalf("key=%q want=from-file", key)
		}
		if !strings.HasPrefix(src, "file:") {
			t.Fatalf("source=%q want file:", src)
		}
	})

	t.Run("empty file errors", func(t *testing.T) {
		p := writeFile("empty", "   \n")
		if _, _, err := loadAPIKey(p, "from-env"); err == nil {
			t.Fatal("expected error for empty file, got nil")
		}
	})

	t.Run("missing file falls back to env", func(t *testing.T) {
		missing := filepath.Join(dir, "does-not-exist")
		key, src, err := loadAPIKey(missing, "env-key")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if key != "env-key" {
			t.Fatalf("key=%q want=env-key", key)
		}
		if src != "env:AMBERFLO_API_KEY" {
			t.Fatalf("source=%q want env:AMBERFLO_API_KEY", src)
		}
	})

	t.Run("empty path uses env", func(t *testing.T) {
		key, src, err := loadAPIKey("", "env-only")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if key != "env-only" {
			t.Fatalf("key=%q want=env-only", key)
		}
		if src != "env:AMBERFLO_API_KEY" {
			t.Fatalf("source=%q", src)
		}
	})

	t.Run("nothing available errors", func(t *testing.T) {
		missing := filepath.Join(dir, "nope")
		if _, _, err := loadAPIKey(missing, ""); err == nil {
			t.Fatal("expected error when neither file nor env is set")
		}
	})
}
