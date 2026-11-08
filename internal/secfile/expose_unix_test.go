//go:build !windows

package secfile

import (
	"os"
	"testing"
)

// expose lets other users read path.
func expose(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, info.Mode().Perm()|0o044); err != nil {
		t.Fatal(err)
	}
}
