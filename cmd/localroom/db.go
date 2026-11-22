package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/jiejhong/GooseCab/internal/protocol"
)

const (
	dbFile    = "room.gcdb"
	dbMagic   = "GooseCab LocalRoom v2\n"
	maxDB     = 8 << 20
	maxEvents = 256
)

var errRoomFull = errors.New("local room history is full")

type roomState struct {
	RoomID string                      `json:"room_id"`
	Events []protocol.Event            `json:"events"`
	Unread map[string][]protocol.Actor `json:"unread_by_event"`
}

// roomDB is used under the server's state lock. The directory lock covers
// separate processes, including while the data file is atomically replaced.
type roomDB struct {
	dir    string
	lock   *os.File
	state  roomState
	failed bool
}

func openRoomDB(dir, room string) (*roomDB, error) {
	if dir == "" {
		return nil, errors.New("local room state directory is required")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("local room state must be a private directory")
	}
	lock, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	opened, err := lock.Stat()
	if err != nil || !os.SameFile(info, opened) || lockRoomDirectory(lock) != nil {
		_ = lock.Close()
		return nil, errors.New("local room state is locked or cannot be locked")
	}
	db := &roomDB{dir: dir, lock: lock}
	if err := db.loadDedicatedDirectory(room); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func (db *roomDB) Close() error { return db.lock.Close() }

func (db *roomDB) loadDedicatedDirectory(room string) error {
	entries, err := os.ReadDir(db.dir)
	if err != nil {
		return err
	}
	var interruptedWrites []string
	for _, entry := range entries {
		if entry.Name() == dbFile {
			continue
		}
		suffix, temporary := strings.CutPrefix(entry.Name(), ".room-")
		_, numberErr := strconv.ParseUint(suffix, 10, 64)
		info, err := entry.Info()
		if !temporary || numberErr != nil || err != nil ||
			!info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return errors.New("local room state directory contains unrelated files")
		}
		interruptedWrites = append(interruptedWrites, filepath.Join(db.dir, entry.Name()))
	}
	if err := db.load(room); err != nil {
		return err
	}
	// Only the installed database is authoritative. An interrupted write may
	// leave an unpublished temp file even when the previous state is intact.
	for _, temp := range interruptedWrites {
		if err := os.Remove(temp); err != nil {
			return err
		}
	}
	return nil
}

func (db *roomDB) load(room string) error {
	path := filepath.Join(db.dir, dbFile)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		db.state = roomState{RoomID: room, Events: []protocol.Event{}, Unread: make(map[string][]protocol.Actor)}
		return db.save(db.state, true)
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > maxDB {
		return errors.New("local room database must be a private, regular bounded file")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxDB+1))
	if err != nil {
		return err
	}
	payload, ok := bytes.CutPrefix(data, []byte(dbMagic))
	if !ok || len(data) > maxDB {
		return errors.New("unrecognized local room database format")
	}
	if decodeJSON(payload, &db.state) != nil || db.state.RoomID != room ||
		db.state.Events == nil || db.state.Unread == nil || len(db.state.Events) > maxEvents {
		return errors.New("invalid database contents or different room")
	}
	eventIDs := make(map[string]bool, len(db.state.Events))
	for i, e := range db.state.Events {
		// V2 originally persisted authenticated browser authors as "user".
		// Normalize on load so replay and every page agree with new writes.
		e.Actor = publicRoomActor(e.Actor)
		if e.RoomID != room || e.Seq != int64(i+1) || e.Type != "chat.message" ||
			(e.Actor.Kind != "human" && e.Actor.Kind != "agent") || e.Actor.ID == "" || e.EventID == "" ||
			eventIDs[e.EventID] || e.TS.IsZero() {
			return errors.New("invalid local room event")
		}
		db.state.Events[i].Actor = e.Actor
		eventIDs[e.EventID] = true
	}
	for id, recipients := range db.state.Unread {
		if !eventIDs[id] || len(recipients) == 0 || len(recipients) > maxParticipants {
			return errors.New("invalid local room unread state")
		}
		seen := make(map[protocol.Actor]bool, len(recipients))
		for _, actor := range recipients {
			if (actor.Kind != "user" && actor.Kind != "agent") || actor.ID == "" || seen[actor] {
				return errors.New("invalid local room unread recipient")
			}
			seen[actor] = true
		}
	}
	return nil
}

func (db *roomDB) save(state roomState, create bool) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if len(data)+len(dbMagic) > maxDB {
		return errRoomFull
	}
	f, err := os.CreateTemp(db.dir, ".room-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(append([]byte(dbMagic), data...))
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	path := filepath.Join(db.dir, dbFile)
	install := os.Rename
	if create {
		// Publish the first full, marked file without replacing an existing
		// file, even if another process created the path during startup.
		install = os.Link
	}
	if err := install(f.Name(), path); err != nil {
		return err
	}
	return db.lock.Sync()
}

func (db *roomDB) append(event protocol.Event, recipients []protocol.Actor) (protocol.Event, error) {
	if db.failed {
		return protocol.Event{}, errors.New("local room database unavailable")
	}
	if len(db.state.Events) >= maxEvents {
		return protocol.Event{}, errRoomFull
	}
	event.Seq = int64(len(db.state.Events) + 1)
	next := db.state
	next.Events = append(next.Events, event)
	if len(recipients) != 0 {
		next.Unread = maps.Clone(next.Unread)
		next.Unread[event.EventID] = recipients
	}
	if err := db.commit(next); err != nil {
		return protocol.Event{}, err
	}
	return event, nil
}

func (db *roomDB) acknowledge(actor protocol.Actor, ids []string) error {
	next := db.state
	var changed bool
	for _, id := range ids {
		recipients := next.Unread[id]
		index := slices.Index(recipients, actor)
		if index < 0 {
			continue
		}
		if !changed {
			next.Unread = maps.Clone(next.Unread)
			changed = true
		}
		if len(recipients) == 1 {
			delete(next.Unread, id)
		} else {
			next.Unread[id] = slices.Delete(slices.Clone(recipients), index, index+1)
		}
	}
	if !changed {
		return nil
	}
	return db.commit(next)
}

func (db *roomDB) commit(next roomState) error {
	if err := db.save(next, false); err != nil {
		// A late I/O error may follow publication of the new file. Close
		// the live view until restart can read the authoritative state.
		if !errors.Is(err, errRoomFull) {
			db.failed = true
		}
		return err
	}
	db.state = next
	return nil
}
