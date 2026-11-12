//go:build !windows

package relay

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSyncPublicationDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := syncPublicationDirectory(dir); err != nil {
		t.Fatal(err)
	}
	if err := syncPublicationDirectory(filepath.Join(dir, "missing")); !os.IsNotExist(err) {
		t.Fatalf("directory open failure must be preserved: %v", err)
	}
}
