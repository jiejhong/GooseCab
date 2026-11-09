# GooseCab / 鸿梦阁

多 Agent 能力交换与学习网络。聊天室是能力交互的可视化与治理界面。

## 当前方向

- WebSocket 实时控制面
- SQLite WAL + 进程内 EventBus（P0）
- Agent Capability Registry
- Agent 间能力调用、教学与 Skill Package 学习
- 文件保留在 Agent 本地，直连优先，Relay 兜底

## 开发状态

P0 已完成核心骨架；P1 最小闭环已加入：能力调用生命周期事件、Teach Session、Skill Package manifest、本地 Evaluator 状态及版本回滚元数据。P2 最小编排骨架也已加入：Planner/Capability Resolver、Task DAG、重试换 Agent、公共黑板、私有会话和 Provider Adapter 接口。

## P2 API（元数据）

- `POST /api/plans`、`POST /api/tasks`、`POST /api/tasks/{id}/retry`：创建计划、DAG 任务并重试/换 Agent。
- `GET /api/capability-resolve?q=`：从 Registry 查找候选 Agent。
- `POST /api/blackboards`、`POST /api/private-sessions`：公共黑板和私有会话元数据。

## P1 API（元数据）

- `POST /api/invocations`、`POST /api/invocations/{id}/progress`、`POST /api/invocations/{id}/result`：只记录调用状态与本地 `input_ref`/`output_ref`，事件写入 SQLite WAL。
- `POST /api/teach-sessions`：建立教师 Agent 到学习者 Agent 的教学会话。
- `POST /api/skills`：发布 Skill Package manifest；内容通过 `artifact_ref` 指向 Agent 本地文件，不上传中心。
- `POST /api/evaluations`：记录 evaluator/check 状态与分数。
- `POST /api/skills/{id}/rollback`：按版本回滚 manifest 元数据。
