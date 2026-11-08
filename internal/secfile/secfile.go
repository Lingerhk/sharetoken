// Package secfile creates and checks files only their user may access: device
// keys and API tokens. On Unix that is the mode bits. On Windows the mode bits
// mean nothing (Go always reports 0666), so it is the file's ACL.
package secfile

import "os"

// Create makes a new file at path that only this user can access, and fails if
// path exists. Access is restricted before anything is written to it.
func Create(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	if err := restrict(path); err != nil {
		f.Close()
		os.Remove(path)
		return nil, err
	}
	return f, nil
}

// WriteFile writes data to a new private file at path, replacing any file there.
func WriteFile(path string, data []byte) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	f, err := Create(path)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}
