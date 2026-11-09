package store

import (
 "context"
 "database/sql"
 "encoding/json"
 "fmt"
 "time"

 _ "modernc.org/sqlite"
 "github.com/jiejhong/GooseCab/internal/protocol"
)

type Store struct{ db *sql.DB }
func Open(path string) (*Store,error){ db,err:=sql.Open("sqlite",path); if err!=nil{return nil,err}; if _,err=db.Exec(`PRAGMA journal_mode=WAL; CREATE TABLE IF NOT EXISTS events (event_id TEXT PRIMARY KEY, room_id TEXT NOT NULL, seq INTEGER NOT NULL, type TEXT NOT NULL, payload TEXT, created_at TEXT NOT NULL); CREATE INDEX IF NOT EXISTS idx_events_room_seq ON events(room_id,seq);`);err!=nil{db.Close();return nil,err}; return &Store{db},nil }
func (s *Store) Close() error{return s.db.Close()}
func (s *Store) Append(ctx context.Context,e protocol.Event) error { p,_:=json.Marshal(e.Payload); _,err:=s.db.ExecContext(ctx,`INSERT INTO events(event_id,room_id,seq,type,payload,created_at) VALUES(?,?,?,?,?,?)`,e.EventID,e.RoomID,e.Seq,e.Type,string(p),e.TS.UTC().Format(time.RFC3339Nano)); return err }
func (s *Store) EventsAfter(ctx context.Context,room string,after int64)([]protocol.Event,error){ rows,err:=s.db.QueryContext(ctx,`SELECT event_id,seq,type,payload,created_at FROM events WHERE room_id=? AND seq>? ORDER BY seq`,room,after);if err!=nil{return nil,err};defer rows.Close();var out []protocol.Event;for rows.Next(){var id,typ,payload,ts string;var seq int64;if err:=rows.Scan(&id,&seq,&typ,&payload,&ts);err!=nil{return nil,err};var pl any;_ = json.Unmarshal([]byte(payload),&pl);t,_:=time.Parse(time.RFC3339Nano,ts);out=append(out,protocol.Event{EventID:id,RoomID:room,Seq:seq,Type:typ,TS:t,Payload:pl})};return out,rows.Err()}
func (s *Store) NextSeq(ctx context.Context,room string)(int64,error){var n sql.NullInt64;err:=s.db.QueryRowContext(ctx,`SELECT MAX(seq) FROM events WHERE room_id=?`,room).Scan(&n);if err!=nil{return 0,err};return n.Int64+1,nil}
func (s *Store) String()string{return fmt.Sprintf("sqlite store")}
