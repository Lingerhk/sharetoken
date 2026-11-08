package secfile

import (
	"os"
	"os/exec"
	"testing"
)

// expose lets every local user read path, as a directory outside the user's
// profile does by default.
func expose(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// *S-1-5-32-545 is BUILTIN\Users, whatever the system's language calls it.
	// On a file, icacls reports success for inheritance flags but grants nothing.
	grant := "*S-1-5-32-545:R"
	if info.IsDir() {
		grant = "*S-1-5-32-545:(OI)(CI)R"
	}
	if out, err := exec.Command("icacls", path, "/grant", grant).CombinedOutput(); err != nil {
		t.Fatalf("icacls: %v: %s", err, out)
	}
}
