package secfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreatedFileIsPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	f, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := CheckPrivate(path); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(path); err == nil {
		t.Fatal("Create replaced an existing file")
	}
}

// A file made private stays so in a directory that others can read.
func TestPrivateInSharedDirectory(t *testing.T) {
	dir := t.TempDir()
	expose(t, dir)
	path := filepath.Join(dir, "secret")
	if err := WriteFile(path, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := CheckPrivate(path); err != nil {
		t.Fatal(err)
	}
}

func TestExposedFileIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	if err := WriteFile(path, []byte("x")); err != nil {
		t.Fatal(err)
	}
	expose(t, path)
	err := CheckPrivate(path)
	if err == nil || !strings.Contains(err.Error(), "accessible by other users") {
		t.Fatalf("got %v", err)
	}
}

func TestWriteFileReplaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte("old and longer"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); string(data) != "new" {
		t.Fatalf("content %q", data)
	}
	if err := CheckPrivate(path); err != nil {
		t.Fatal(err)
	}
}

func TestCheckMissingFile(t *testing.T) {
	if err := CheckPrivate(filepath.Join(t.TempDir(), "none")); !os.IsNotExist(err) {
		t.Fatalf("got %v", err)
	}
}
