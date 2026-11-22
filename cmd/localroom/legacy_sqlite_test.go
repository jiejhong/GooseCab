//go:build localroom_sqlite_integration

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/jiejhong/GooseCab/internal/store"
)

func TestLegacyStoreRefusesLocalRoomDatabase(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private-state")
	db, err := openRoomDB(dir, testRoom)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.append(fixtureEvent(), nil); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, dbFile)
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := store.Open(path)
	if err == nil {
		_ = legacy.Close()
		t.Fatal("legacy gateway store opened the local room database")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(contents, after) {
		t.Fatal("legacy gateway altered the independent database")
	}
}
