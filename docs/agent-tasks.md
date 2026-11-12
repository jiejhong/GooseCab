# 真实 Agent 文本任务：执行、取结果与中断恢复

本文描述最初的基础小闭环：调用方提交文本，指定 worker 真正执行 Unicode 大写转换，返回可读取的文本结果。不是任意 shell、文件读取、网络请求或 LLM。另有显式 [能力学习](capability-learning.md)、[DAG 调度](task-orchestration.md)、[级联](cascade.md) 和 [流式传输](streaming-relay.md)，都不由此基础调用隐式启用。

## 三个终端完成一次真实调用

在仓库根目录运行，要求 Go 1.23+。以下例子假设未开启 token，仅用于可信本机环境；远程部署必须使用 WSS 和访问控制。

```bash
# 终端 1：Gateway，控制面和观察页使用同一端口
mkdir -p .data
GOOSECAB_DB="$PWD/.data/goosecab.db" go run ./cmd/server -addr 127.0.0.1:8312

# 终端 2：显式启用唯一的安全样例能力
# 必须保留这个日志文件，重启时使用同一个 -state。
go run ./cmd/agent -mode worker -room demo -id text-worker \
  -state "$PWD/.data/text-worker.db"

# 终端 3：单次提交，等待最多 30 秒；stdout 是结果 JSON
go run ./cmd/invoke -room demo -id caller -target text-worker \
  -request-id demo-text-001 -text 'Hello 鸿梦阁'
```

预期结果：

```json
{"result_available":true,"output":{"text":"HELLO 鸿梦阁","media_type":"text/plain"},"request_id":"demo-text-001","status":"completed"}
```

每个命令支持 `-url`；token 默认从 `GOOSECAB_AGENT_TOKEN` 读取，也支持 `-token`。不要把秘密写入 `-text`：输入和小文本输出会作为房间事件持久化，对有权限的房间读者可见。命令行参数也可能被本机其他进程看到。

浏览器打开 `http://127.0.0.1:8312/?room_id=demo`，任务卡将同一调用 ID 的请求、进度和结果聚合在一起。只有 `status=completed`、`result_available=true` 且存在 `output.text` 时，才显示可展开的纯文本结果。观察页使用脱敏历史，可能遮盖路径、凭据格式等文本；CLI 返回原始的小文本结果。不要把脱敏阅读副本当作逐字无损文件交付。

## 状态必须区分

- **请求已记录**：Gateway 保存 `capability.invoke`。不等于 worker 在线，也不等于已经执行。
- **正在执行**：worker 已持久化占用调用 ID，发送 `capability.progress`，`status=running`。
- **执行完成**：worker 已保存结果，发送 `capability.progress`，`status=completed`、`percent=100`。此时 Gateway 还不一定拿到结果。
- **结果可取**：Gateway 持久化 `capability.result`，`status=completed`、`result_available=true`、`output={text,media_type:"text/plain"}`。此时能从 CLI 或房间记录读取结果。
- **失败**：输入校验失败等已知失败，`status=failed`、`result_available=false`，并附带 `error`。
- **不确定**：日志中已经占用执行 ID，但没有持久结果，`status=unknown`。不能据此断言“没有执行”，更不能自动重跑。

`request_id` 和事件 `correlation_id` 始终相同。调用 ID 在房间内唯一，worker 日志按 `(room, agent, request_id)` 去重，并绑定原始调用者、能力、目标、payload 的指纹。不同房间的同名 ID 不互相影响。

## 断线与重复操作

1. `cmd/invoke` 只提交一次。超时或断线会退出并提示结果不确定，不自动重发，也不替你生成新 ID。
2. worker 断线会退出。修复连接后，用原命令和**同一日志文件**重启；Gateway 回放原请求。Gateway 已有明确完成/失败结果的历史调用会跳过；只有尚未确认可取的已保存结果才重新报告，不执行第二次。
3. 用完全相同的 `-room`、`-id`、`-target`、`-request-id` 和 `-text` 再运行 `cmd/invoke`，可取回结果。若 welcome 前历史已有完成/失败，CLI 直接返回最新已确认结果，不再提交。回放中较早的 unknown 不覆盖后来的 completed。
4. 同 ID 改目标、调用者、能力或输入会被拒绝。要做另一个有意的新任务，才使用新 ID。
5. 日志有占用但无持久结果时，重启只报告 unknown。先检查 worker 日志和已持久化事件；本版本不提供“强制重跑不确定任务”的按钮。
6. 如果之前只报 unknown，随后从原执行的持久结果中恢复出 completed，Gateway 允许状态得到确认。已确认 completed/failed 的内容不能再被修改。
7. 新建、删除或丢失 worker 日志会失去去重保证。不得为了消除 unknown 而换一个空日志重放旧房间；不要在多个 Gateway 间共用日志或并行运行同一 Agent ID 的多个 worker。日志备份需要一起保留 SQLite WAL 数据。

可手动验证：完成一次调用后停止 worker，用同一 ID 再调用，仍可从 Gateway 取结果；然后用原日志重启 worker，事件中不应出现第二个 running。离线 worker 的全新任务会等待/超时，但未来 worker 上线并回放后仍可能执行，所以超时后不要立即用新 ID 重发。

## 安全和兼容性

- worker 是显式 opt-in；默认 read-only 和 interactive 模式仍不会执行任务。worker 默认只声明 `cap.text.uppercase`；显式 `-skills` 可加入已评估的本地声明式文本能力。禁止用 `-capabilities` 任意扩展执行范围。
- 必须显式指定目标。worker 忽略其他房间、其他目标、无目标、非该能力的事件。payload 仅允许 `{"text":"..."}`，输入及输出各最多 4096 UTF-8 字节；内容始终当文本，不求值、不调用 shell、不访问文件或外网。
- 小文本结果是有意写入事件的受限数据，不是文件数据面。原有 `output_ref` 只是本地引用，绝不能被解释为文件已交付；本迭代不会下载这些引用。
- Gateway 绑定新能力的调用者和目标，拒绝 ID 冲突、非目标上报、状态倒退与修改最终结果。三种 capability 生命周期必须使用专用帧，不能再经 `event.publish` 绕过检查。旧元数据能力和 P1/P2 HTTP 接口继续保留，它们不会启动此 worker。
- 共享 token 仍不是每 Agent 独立认证或房间 ACL；有 token 的人可冒用首次声明的 Agent ID。只用于互信成员，不声称已具备多租户隔离。
- 当前要求单 Gateway 进程，执行校验按房间/调用索引读取历史。一个调用达到 2048 条生命周期事件就拒绝继续写入，防止无限重复历史拖垮校验。拒绝携带调用 ID，仅阻断该调用，不停止 worker 的其他任务；旧的已终结回放不会重复上报。不是高吞吐任务调度器。
- Registry 的能力列表不代表在线状态；重连后只按 worker 当前启用能力执行。无人执行不会自动换 Agent。
- 还没有自动重连、结果保留策略、任务取消、人工审批重试、分布式 exactly-once或通用 Agent Sandbox。显式文件 Relay 已独立实现，但此文本调用不会下载文件引用。以上不应从这个文本样例推断为已经实现。

## 回归验收

```bash
gofmt -w cmd internal
go vet ./...
go test ./...
go test -race ./...
node tests/observer.test.cjs
```

测试覆盖真实 Gateway/worker/caller 往返、重复/并发重复、日志重启、结果发送中断、执行结果未知不重跑、超时不自动重试、跨房间/目标隔离、输入白名单/大小限制、调用 ID 冲突、结果归属、状态校验、错误事件伪装、历史 unknown 后完成的恢复，长房间历史索引查询、2048条边界及饱和旧调用不阻断其他任务。UI 测试检查任务聚合、执行完成与可取结果分离、未知提示、纯文本渲染、分页与重连行为。
