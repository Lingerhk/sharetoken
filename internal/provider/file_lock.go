package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"time"

	"sharetoken/internal/secfile"
)

// The lock file stays in place: unlinking an advisory lock permits different
// processes to lock different inodes. OS locks are released after a crash.
func lockProviderFile(ctx context.Context, home, key string) (func(), error) {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(key))
	path := filepath.Join(home, ".providers-"+hex.EncodeToString(sum[:12])+".lock")
	f, err := secfile.Create(path)
	if errors.Is(err, os.ErrExist) {
		f, err = os.OpenFile(path, os.O_WRONLY, 0o600)
	}
	if err != nil {
		return nil, err
	}
	for {
		ok, err := tryProviderLock(f)
		if err != nil {
			f.Close()
			return nil, err
		}
		if ok {
			return func() { unlockProviderFile(f); f.Close() }, nil
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}
