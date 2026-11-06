package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCheckStaticDirRefusesFolderWithoutIndex(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "main.go"), "package main")
	writeFile(t, filepath.Join(dir, "sub", "index.html"), "<p>")

	_, _, err := checkStaticDir(dir, false)
	if err == nil || !strings.Contains(err.Error(), "--no-index") {
		t.Fatalf("expected refusal pointing at --no-index, got %v", err)
	}
}

func TestCheckStaticDirNoIndexOverride(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "file.txt"), "abc")

	files, size, err := checkStaticDir(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if files != 1 || size != 3 {
		t.Errorf("expected 1 file of 3 bytes, got %d files, %d bytes", files, size)
	}
}

func TestCheckStaticDirCountsWithoutGit(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "index.html"), "<p>hi</p>")
	writeFile(t, filepath.Join(dir, ".git", "config"), "secret stuff")

	files, size, err := checkStaticDir(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if files != 1 || size != 9 {
		t.Errorf("expected only index.html counted, got %d files, %d bytes", files, size)
	}
}

func TestCheckStaticDirMissing(t *testing.T) {
	if _, _, err := checkStaticDir(filepath.Join(t.TempDir(), "nope"), true); err == nil {
		t.Fatal("expected error for missing directory")
	}
}
