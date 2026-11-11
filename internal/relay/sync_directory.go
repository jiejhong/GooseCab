//go:build !windows

package relay

import "os"

// syncPublicationDirectory preserves filesystem errors rather than treating all
// directory-sync failures as an unsupported-platform fallback.
func syncPublicationDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	if err = dir.Sync(); err != nil {
		_ = dir.Close()
		return err
	}
	return dir.Close()
}
