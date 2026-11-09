# GooseCab 控制面协议

本文记录当前 P0-P2 实现实际支持的消息和元数据接口。协议仍会演进，客户端应忽略不认识的字段。

## WebSocket 连接

Agent 主动连接 Gateway，不需要开放入站端口：

```text
ws://gateway.example.com/api/v1/agent/connect?room_id=demo
```

观察页使用同一套事件流：

```text
wss://gateway.example.com/ws?room_id=demo&resume_seq=0
```

`resume_seq` 是房间内最后已经处理的事件序号。Gateway 会先回放 `seq > resume_seq` 的事件，再广播新事件。两条路径都支持可选的 `Authorization: Bearer <token>`；当 `GOOSECAB_AGENT_TOKEN` 非空时必须发送该 header。

## 建连握手

客户端连上后发送：

```json
{
  "type": "agent.hello",
  "agent_id": "agent-a",
  "name": "Echo Agent",
  "role": "worker",
  "capabilities": ["cap.echo"]
}
```

Gateway 回复：

```json
{
  "type": "agent.welcome",
  "room_id": "demo",
  "resume_from": 0
}
```

随后 Gateway 将 hello 和每个 capability 写入房间事件流。事件帧的基本结构是：

```json
{
  "event_id": "capability.announce-2",
  "room_id": "demo",
  "seq": 2,
  "type": "capability.announce",
  "actor": {"kind": "agent", "id": "agent-a"},
  "capability_id": "cap.echo",
  "correlation_id": "",
  "ts": "2026-11-09T00:00:00Z",
  "payload": {"agent_id": "agent-a", "capability_id": "cap.echo"}
}
```

`seq` 只在同一个 `room_id` 内递增。客户端应使用 `(room_id, seq)` 去重，并在重新连接时带上最近一次确认的序号。

## 能力消息

Agent 可以直接发送以下 JSON 帧。Gateway 会把它们写入事件流并广播给同房间连接；当前 Gateway 不替 Agent 执行 capability。

```json
{"type":"capability.announce","agent_id":"agent-a","capability_id":"cap.echo","name":"Echo","version":"1"}
{"type":"capability.invoke","request_id":"req-1","capability_id":"cap.echo","target_agent_id":"agent-a","payload":{"text":"hello"}}
{"type":"capability.progress","request_id":"req-1","percent":50,"message":"working"}
{"type":"capability.result","request_id":"req-1","status":"completed","output_ref":"file:///agent-a/out.json"}
```

`input_ref`、`output_ref`、`artifact_ref` 是 Agent 本地引用。Gateway 只保存引用字符串，不读取引用指向的文件。

## 文件传输元数据

文件传输的控制面只传元数据，文件字节由 Agent 之间的 direct 或 relay 数据面实现：

```json
{
  "type": "transfer.request",
  "file_id": "file-1",
  "source_agent": "agent-a",
  "source_path_ref": "/var/lib/agent-a/out.bin",
  "target_agent": "agent-b",
  "target_path_ref": "/var/lib/agent-b/in.bin",
  "size": 42,
  "sha256": "...",
  "mode": "direct",
  "offset": 0,
  "status": "requested"
}
```

可广播的传输状态类型为 `transfer.accepted`、`transfer.progress`、`transfer.completed` 和 `transfer.failed`。状态更新应携带同一个 `file_id` 或 `transfer_id`。

## HTTP 元数据接口示例

注册 Agent 时，Registry 的 `capabilities` 是对象数组：

```bash
curl -X POST http://127.0.0.1:8080/api/agents \
  -H 'Content-Type: application/json' \
  -d '{"id":"agent-a","name":"Echo Agent","capabilities":[{"id":"cap.echo","name":"Echo","version":"1"}]}'
```

P1 调用生命周期示例：

```bash
curl -X POST http://127.0.0.1:8080/api/invocations \
  -H 'Content-Type: application/json' \
  -d '{"id":"req-1","room_id":"demo","capability_id":"cap.echo","caller_agent_id":"agent-b","target_agent_id":"agent-a","input_ref":"file:///agent-b/in.json"}'

curl -X POST http://127.0.0.1:8080/api/invocations/req-1/progress \
  -H 'Content-Type: application/json' -d '{"percent":50,"message":"working"}'

curl -X POST http://127.0.0.1:8080/api/invocations/req-1/result \
  -H 'Content-Type: application/json' -d '{"status":"completed","output_ref":"file:///agent-a/out.json"}'
```

P2 的 plan/task/blackboard/private-session 接口当前记录编排元数据，不会启动调度器或 Provider 进程。
