//go:build !windows

package secfile

import (
	"fmt"
	"os"
)

// restrict has nothing to do: Create's mode 0600 already says it.
func restrict(string) error { return nil }

// CheckPrivate returns an error, with the command that fixes it, if other users
// can access the file at path.
func CheckPrivate(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s is accessible by other users; run: chmod 600 %s", path, path)
	}
	return nil
}
