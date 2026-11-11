package relay

import "testing"

// The complete ReceiveFile tests run on Windows CI as well. This isolates the
// intentional metadata-durability exception from file syncing and publication.
func TestWindowsDirectorySyncUnsupported(t *testing.T) {
	if err := syncPublicationDirectory(t.TempDir()); err != nil {
		t.Fatal(err)
	}
}
