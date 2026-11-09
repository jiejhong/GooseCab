package agent

import (
 "crypto/sha256"
 "encoding/hex"
 "fmt"
 "io"
 "os"
 "time"
 "github.com/gorilla/websocket"
)
// Client is a minimal outbound Agent Node connection. It never exposes an inbound port.
type Client struct { Conn *websocket.Conn; AgentID string; RoomID string }
func Dial(url, room, id string, caps []string)(*Client,error){c,_,e:=websocket.DefaultDialer.Dial(url+"?room_id="+room,nil);if e!=nil{return nil,e};x:=&Client{Conn:c,AgentID:id,RoomID:room};if e=c.Write(map[string]any{"type":"agent.hello","agent_id":id,"capabilities":caps});e!=nil{c.Close();return nil,e};c.SetReadDeadline(time.Now().Add(2*time.Second));var welcome map[string]any;if e=c.ReadJSON(&welcome);e!=nil||welcome["type"]!="agent.welcome"{c.Close();return nil,fmt.Errorf("agent welcome: %v",e)};c.SetReadDeadline(time.Time{});return x,nil}
func(c *Client)Write(v any)error{return c.Conn.WriteJSON(v)}
func(c *Client)Read() (map[string]any,error){var v map[string]any;e:=c.Conn.ReadJSON(&v);return v,e}
func(c *Client)Close()error{return c.Conn.Close()}
// CopyLocal is the data-plane primitive used by direct or relay implementations. The gateway sees only metadata.
func CopyLocal(src,dst string)(size int64,sha string,err error){in,e:=os.Open(src);if e!=nil{return 0,"",e};defer in.Close();out,e:=os.Create(dst);if e!=nil{return 0,"",e};h:=sha256.New();n,e:=io.Copy(io.MultiWriter(out,h),in);if ce:=out.Close();e==nil{e=ce};return n,hex.EncodeToString(h.Sum(nil)),e}
