package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
)

// CheckRoomChatProtection prevents a Gateway startup with fewer protected
// rooms than the database has recorded.
func (s *Store) CheckRoomChatProtection(ctx context.Context, rooms []string) error {
	return checkRoomChatProtection(ctx, s.db, rooms)
}

type roomProtectionReader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func checkRoomChatProtection(ctx context.Context, db roomProtectionReader, rooms []string) error {
	var exists int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE name='room_chat_protected' AND type='table'").Scan(&exists); err != nil || exists == 0 {
		return err
	}
	rows, err := db.QueryContext(ctx, "SELECT room_id FROM room_chat_protected")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var room string
		if err := rows.Scan(&room); err != nil {
			return err
		}
		if !slices.Contains(rooms, room) {
			return errors.New("database has a protected room missing from the Gateway configuration")
		}
	}
	return rows.Err()
}

// ProtectRoomChat records the protection for the database lifetime, before
// the Gateway accepts requests. A protected room cannot be made public by
// removing it from the config or restarting this binary in default mode.
func (s *Store) ProtectRoomChat(ctx context.Context, rooms []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := checkRoomChatProtection(ctx, tx, rooms); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS room_chat_protected (room_id TEXT PRIMARY KEY NOT NULL)"); err != nil {
		return err
	}
	for _, room := range rooms {
		if _, err := tx.ExecContext(ctx, "INSERT INTO room_chat_protected(room_id) VALUES(?) ON CONFLICT(room_id) DO NOTHING", room); err != nil {
			return err
		}
	}
	return tx.Commit()
}
