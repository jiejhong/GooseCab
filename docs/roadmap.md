# GooseCab / 鸿梦阁 P0-P2 功能路线

## P0：能力交换内核

目标：两个 Agent 可以注册能力、互相发现、双向调用，并通过本地直连或 Relay 传输 file_ref。中心只保存事件和传输元数据。

验收：A→B、B→A 双向调用；一次本地文件传输；Web 时间线可回放。

## P1：能力学习与复用

初始元数据阶段：Teach Session、Skill Package manifest、调用 request/progress/result 事件、Evaluator 状态、版本/回滚元数据。后续再接 Skill Compiler、本地 Sandbox 和 KnowledgeCard。当前能力包可通过 HTTPS/WSS Relay 流式传输，不是对 Gateway 的端到端加密；详见下方已实现核心。

验收：B 教 A 一项能力，A 本地验证通过并在断开 B 后独立复用。

## P2：多 Agent 协作网络

初始元数据阶段：Planner/Capability Resolver、Task DAG、重试/换 Agent、公共黑板、私有会话元数据和 Provider Adapter 接口；当前受限真实 DAG 见下方已实现核心。

加入 Planner、Capability Resolver、任务 DAG、并发/重试、公共黑板、私有会话、结果编排、多房间、Provider Adapter、Relay 网络化、观测和能力治理。

验收：给定一个目标，系统自动选择多个 Agent 并行协作，失败可重试、换 Agent 或人工接管，全过程可回放。


## 当前迭代：先验收一个真实任务

已实现显式 `cap.text.uppercase` worker、单次调用 CLI、持久去重、unknown 不自动重试，以及按调用 ID 聚合的任务卡。执行完成与结果可取分开展示，小文本结果可从 CLI 或页面读取。操作和验收见 [真实 Agent 任务](agent-tasks.md)。

这不把上述 P1 学习验收和 P2 并行协作验收改成“已通过”。独立 Agent 身份/房间权限、在线会话状态和人工重试决策仍待实现；受限真实调度和文件交付已单独加入下方核心。


## 已实现的受限真实协作核心

- `cmd/plan`：显式目标和受限文本能力的 DAG 并发执行（默认 uppercase，可显式选择已安装 recipe）、依赖输入、持久任务跟踪、未知不重试和只读恢复。不是自然语言规划器，不自动换 Agent。
- `cmd/skill`：SHA256 钉住的受限文本 recipe，通过真实 Relay 到 Learner 本地，评估后激活，可离线执行/回滚。不是模型训练或任意代码安装。
- `cmd/cascade`：Leader 和命名空间 flat 代理，两 Gateway 真实能力调用、进度/结果关联、循环/跳数限制与未知恢复。
- `cmd/transfer`：仅出站 WebSocket、有界背压流式传输，接收端完整性验证和安全落盘；Gateway 不保存文件内容。

`python3 tests/collaboration-smoke.py` 验收以上功能组合，包含多个真实 OS 进程与 TCP 连接。独立物理主机、WAN/TLS、生产身份/房间权限、通用任务/模型能力、自动重试/分配仍需另外验收，不把单机组合测试等同这些目标完成。


## 新增 Skill / MCP / 知识核心

一等类型目录、真实 Skill 文件包和本机审批、显式批准的 HTTP MCP 调用，以及默认私有的知识/经验记录与选择性传递已实现。目录描述不等于安装授权；接收包不等于激活；调用远端能力不等于学习安装。完整文件经 Relay，网关不持久化包文件或知识正文。受限 Skill recipe 可执行，通用脚本 Skill 仍为手工说明包；MCP 限 JSON HTTP 子集；知识学习为持久记录导入，不是模型训练。`tests/providers-smoke.py` 单机多进程验收包括两 Gateway 的三类能力代理调用。
