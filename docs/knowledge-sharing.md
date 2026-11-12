# Agent 知识、记忆与经验共享

此功能共享的是 Agent 显式录入的**知识记录**，不是磁盘容量或文件系统信息。记录只存在 Agent 本地 SQLite 中，不会自动扫描用户文件或读取其他私人记忆，也不会训练模型权重。内容始终是待审阅的数据，不能作为执行指令、权限或可信系统提示。

## 隐私与授权边界

- 默认私有。只有 `share` 明确选择的记录版本、SHA-256 和命名接收者可导出或查询。没有默认全集导出、通配接收者或自动共享新版本。
- `cap.knowledge.<collection>` 仅暴露已共享集合。查询由执行器传入的原始调用 Actor 授权，不能从输入 payload 伪造 caller；缺少 Actor 时拒绝。
- 远程查询只匹配已选记录的标题，返回记录引用、标题和 source 的 SHA-256 标识，不返回正文、原始 source（可能包含路径/敏感 URL）、标签或正文片段。输出最多 4096 字节，最多 8 条；`truncated` 提示还有匹配结果。
- **Gateway 房间观察者可以看到查询和结果元数据**。按 caller 的 allowlist 不是房间内的保密隔离。请使用仅可信参与者可访问的房间；共享 Gateway token 及 Agent ID 不是独立密码学身份认证。
- 完整记录只通过现有 Relay 文件数据面传输。Gateway 不持久化文件内容。传输 ticket 必须私密交付，不放在聊天事件中。
- 接收只写入暂存文件。用户在本地 `review` 审阅全部记录及来源后，用 `import -approve-sha256` 明确批准这些精确字节。摘要仅验证完整性；来源及 provenance 是发送者声明，**不是数字签名或事实真实性证明**。
- 导入不带入共享策略。接收者需要另一次独立 `share` 才能再分享。接收者字段也绑定包，不能把 Bob 的包批准给 Carol。
- 相同来源/ID/版本/内容去重；相同版本不同内容拒绝；新版本保存历史；未知旧版本回滚拒绝。重复导入已批准包是无副作用操作，不会替换较新版本。包内操作事务化。
- 撤销集合阻止后续查询和导出，不撤回已经发送的文件或房间历史。重启 worker 更新已公告集合；现有执行器的本地检查仍会拒绝已撤销集合。

## 本地录入与明确分享

以下只使用合成示例，无真实用户记忆或凭据。数据库文件和其父目录应在 Agent 自己的私有目录；不要共享数据库或 WAL 文件。每个数据库绑定一个 Agent ID。

```bash
mkdir -p .data/alice .data/bob
chmod 700 .data/alice .data/bob
# 显式输入单条记录，返回用于批准的精确 Ref
 go run ./cmd/knowledge -mode put -id alice \
   -knowledge .data/alice/knowledge.db -file examples/knowledge/retry-lesson.json \
   > .data/alice/ref.json
# 仅选择此 Ref；允许 bob 查询/接收 lessons
python3 - <<'PY'
import json
ref = json.load(open('.data/alice/ref.json'))
json.dump({'name':'lessons','records':[ref],'callers':['bob']},
          open('.data/alice/share.json','w'))
PY
go run ./cmd/knowledge -mode share -id alice \
  -knowledge .data/alice/knowledge.db -file .data/alice/share.json
# 预览导出，不会发送；新文件不能覆盖旧文件
go run ./cmd/knowledge -mode export -id alice \
  -knowledge .data/alice/knowledge.db -collection lessons \
  -recipient bob -file .data/alice/lessons.json
```

记录包括 ID、origin、整数 version、title、body、tags、source、provenance_ids、created_at、updated_at。省略 origin 时使用本地 Agent ID，省略时间时使用当前 UTC 时间；每个版本不可变。修改记录需要提高 version，并重新选择精确 Ref 以分享新版本。

## Relay → 暂存 → 审阅 → 明确学习

启动正常 Gateway 后，在 Alice 运行：

```bash
go run ./cmd/knowledge -mode send -id alice \
  -knowledge .data/alice/knowledge.db -collection lessons -recipient bob \
  -ticket .data/alice/bob-ticket.json -url http://127.0.0.1:8312
```

通过安全的带外渠道将 receiver-only ticket 交给 Bob，并核对发送者、接收者、集合及 SHA-256。在 Bob 运行（`receive` 不需要打开知识库，也不导入内容）：

```bash
go run ./cmd/knowledge -mode receive -id bob \
  -ticket .data/bob/bob-ticket.json -stage-dir .data/bob \
  -url http://127.0.0.1:8312
# 上一步打印实际暂存文件路径。替换下方占位符。
go run ./cmd/knowledge -mode review -file STAGED_FILE
# 审阅后，以 review 的精确 sha256 作独立批准。
go run ./cmd/knowledge -mode import -id bob \
  -knowledge .data/bob/knowledge.db -file STAGED_FILE \
  -approve-sha256 REVIEWED_SHA256
# 即使 Alice 和 Gateway 下线，本地仍可检索正文及原始 provenance。
go run ./cmd/knowledge -mode search -id bob \
  -knowledge .data/bob/knowledge.db -query retry
```

`review` 显示完整正文，仅应在私有终端中执行。`export` 产生的文件也包含完整正文，应按私密资料处理。可先在接收端查阅合成文件以了解结构。

## Worker 与撤销

```bash
go run ./cmd/agent -mode worker -id alice -room private-demo \
  -state .data/alice/execution.db -knowledge-store .data/alice/knowledge.db
# 调用 cap.knowledge.lessons，payload 为 {"query":"Retry"}，调用者必须为 bob。
# 停止共享：
go run ./cmd/knowledge -mode unshare -id alice \
  -knowledge .data/alice/knowledge.db -collection lessons
```

限制：单条记录 JSON 最大 32 KiB；每包最多 512 条、32 MiB；标签和 provenance 各最多 32 项；caller 最多 64 个。来源审查、提示注入防护、事实校验和使用时的人类判断仍然必要。

## 验证

`go test ./internal/knowledge` 使用两个独立 Agent 目录，执行真实 WebSocket Relay → 暂存 → 审阅摘要 → 显式导入 → 关闭源端/Gateway → 离线检索，另覆盖私有不可发现、caller 伪造/未授权拒绝、篡改、冲突、新版本、重复导入、非自动再分享和大小限制。这是本地回环网络测试，不等于已验收两台物理机器或生产 TLS。
