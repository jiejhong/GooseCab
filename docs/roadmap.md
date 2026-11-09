# GooseCab / 鸿梦阁 P0-P2 功能路线

## P0：能力交换内核

两个 Agent 可以注册能力、互相发现、双向调用，并通过本地直连或 Relay 传输 file_ref。中心只保存事件和传输元数据。

验收：A→B、B→A 双向调用；一次本地文件传输；Web 时间线可回放。

## P1：能力学习与复用

已加入最小闭环：Teach Session、Skill Package manifest、调用 request/progress/result 事件、Evaluator 状态、版本/回滚元数据。后续再接 Skill Compiler、本地 Sandbox 和 KnowledgeCard。能力包从 Agent 本地直连或经 Relay 加密分片传输。

验收：B 教 A 一项能力，A 本地验证通过并在断开 B 后独立复用。

## P2：多 Agent 协作网络

加入 Planner、Capability Resolver、任务 DAG、并发/重试、公共黑板、私有会话、结果编排、多房间、Provider Adapter、Relay 网络化、观测和能力治理。

验收：给定一个目标，系统自动选择多个 Agent 并行协作，失败可重试、换 Agent 或人工接管，全过程可回放。
