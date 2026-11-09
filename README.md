# GooseCab / 鸿梦阁

多 Agent 能力交换与学习网络。聊天室是能力交互的可视化与治理界面。

## P0 当前实现

- Agent Node 通过主动出站 WSS 连接 /api/v1/agent/connect，不要求 Agent 暴露入站端口。
- 首帧 agent.hello 携带 agent_id、角色、resume_seq 和能力摘要；服务端回 agent.welcome。
- 能力通过 Agent Registry 发布、查询，调用事件走统一 protocol。
- SQLite WAL 事件存储、进程内 EventBus、WebSocket 实时控制面。
- 文件始终保留在 Agent 本地，直连优先，Relay 兜底；中心只保存 FileRef/Transfer 元数据。

### 握手示例

{"type":"agent.hello","agent_id":"analyzer-01","role":"analyzer","resume_seq":42,"capabilities":["video.inspect"]}
{"type":"agent.welcome","session_id":"sess-1","resume_from":42}
