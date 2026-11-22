package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLocalRoomFixtureProcess(t *testing.T) {
	if os.Getenv("GOOSECAB_LOCALROOM_TEST_PROCESS") != "1" {
		return
	}
	if n := len(os.Args); n >= 4 && os.Args[n-3] == "--" {
		if err := runLocalRoom(context.Background(), os.Args[n-2], os.Args[n-1], 0); err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Fatal("missing fixture arguments")
}

func TestIndependentProcessLockAndKilledProcessRecovery(t *testing.T) {
	fixture := t.TempDir()
	config, state := filepath.Join(fixture, "test-policy.json"), filepath.Join(fixture, "private-state")
	expiry := time.Now().Add(time.Minute)
	writer, first, second := fixtureToken(80), fixtureToken(81), fixtureToken(82)
	broadcaster := fixtureParticipant("user", "writer", writer, expiry, true, true)
	broadcaster.Broadcast = true
	configData, err := json.Marshal(localConfig{Enabled: true, RoomID: testRoom, Participants: []participant{
		broadcaster, fixtureParticipant("user", "first", first, expiry, true, false),
		fixtureParticipant("user", "second", second, expiry, true, false),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, configData, 0600); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	newProcess := func(ctx context.Context) *exec.Cmd {
		proc := exec.CommandContext(ctx, binary, "-test.run=^TestLocalRoomFixtureProcess$", "--", config, state)
		proc.Env = append(os.Environ(), "GOOSECAB_LOCALROOM_TEST_PROCESS=1")
		return proc
	}
	start := func() (*exec.Cmd, string) {
		t.Helper()
		proc := newProcess(context.Background())
		pipe, err := proc.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		proc.Stderr = proc.Stdout
		if err := proc.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if proc.ProcessState == nil {
				_ = proc.Process.Kill()
				_ = proc.Wait()
			}
		})
		line := make(chan string, 1)
		go func() {
			scanner := bufio.NewScanner(pipe)
			if scanner.Scan() {
				line <- scanner.Text()
			} else {
				line <- ""
			}
		}()
		select {
		case value := <-line:
			_, address, ok := strings.Cut(value, "listening at ")
			if !ok || !strings.HasPrefix(address, "http://127.0.0.1:") {
				t.Fatalf("fixture process did not start: %q", value)
			}
			return proc, address
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for fixture process")
			return nil, ""
		}
	}

	child, address := start()
	event := postMentionFixture(t, address, writer, `{"text":"survives process kill","mention_all":true}`, http.StatusCreated)
	markMentions(t, address, first, event.EventID)
	before, err := os.ReadFile(filepath.Join(state, dbFile))
	if err != nil {
		t.Fatal(err)
	}
	// Simulate an unfinished staging file from an interrupted write. The
	// installed database is complete and must remain the source of truth.
	temp := filepath.Join(state, ".room-123456")
	if err := os.WriteFile(temp, []byte("interrupted temporary write"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	result, err := newProcess(ctx).CombinedOutput()
	cancel()
	if err == nil || !bytes.Contains(result, []byte("local room state is locked")) {
		t.Fatalf("second live process was not rejected by the lock: %s", result)
	}

	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); err == nil || child.ProcessState.Success() {
		t.Fatal("test did not terminate via process kill")
	}
	_, afterRestart := start()
	if firstPage := fetchMentions(t, afterRestart, first, ""); firstPage.UnreadCount != 0 {
		t.Fatal("persisted read acknowledgement was lost after process kill")
	}
	if next := fetchMentions(t, afterRestart, second, ""); next.UnreadCount != 1 ||
		next.Events[0].EventID != event.EventID {
		t.Fatal("persisted event or unread receipt was lost after process kill")
	}
	after, err := os.ReadFile(filepath.Join(state, dbFile))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("recovery rewrote the authoritative committed database")
	}
	if _, err := os.Stat(temp); !os.IsNotExist(err) {
		t.Fatal("interrupted staging file was not cleaned up")
	}
}

func TestCandidateRefusesFirstStageDatabase(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, dbFile)
	old := []byte(fmt.Sprintf("GooseCab LocalRoom v1\n{\"room_id\":%q,\"events\":[]}", testRoom))
	if err := os.WriteFile(path, old, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := openRoomDB(dir, testRoom); err == nil {
		t.Fatal("candidate accepted a first-stage database")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(old, after) {
		t.Fatal("candidate changed a first-stage database")
	}
}

func TestSQLiteRejectsInstalledRoomDatabase(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("system SQLite probe requires Python")
	}
	if err := exec.Command(python, "-c", "import sqlite3").Run(); err != nil {
		t.Skip("system SQLite probe requires Python sqlite3")
	}
	dir := filepath.Join(t.TempDir(), "state")
	db, err := openRoomDB(dir, testRoom)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.append(fixtureEvent(), nil); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, dbFile)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Exercise the SQLite engine with the same initial PRAGMAs as store.Open.
	const check = `
import sqlite3
import sys
try:
    with sqlite3.connect(sys.argv[1]) as db:
        db.execute("PRAGMA busy_timeout=5000")
        db.execute("PRAGMA journal_mode=WAL").fetchall()
        db.execute("SELECT name FROM sqlite_master").fetchall()
except sqlite3.DatabaseError as error:
    if 'file is not a database' not in str(error):
        raise
else:
    raise AssertionError("SQLite opened an independent room database")
`
	probe := exec.Command(python, "-c", check, path)
	if output, err := probe.CombinedOutput(); err != nil {
		t.Fatalf("SQLite did not reject the local room file: %s", output)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("SQLite modified the installed room database")
	}
}
