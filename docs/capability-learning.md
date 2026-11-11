# 真实能力传递与本地学习

这不是模型训练或任意代码安装。Teacher 发送受限声明式文本 recipe；Learner 校验预期 SHA256、能力名、版本，执行样例评估，然后把 package 和激活版本原子写入自己的 SQLite 文件。之后即使 Teacher 与 Gateway 都离线，Learner 仍能独立运行。

## 安全边界

- Package 是最多 64KiB 的 JSON，format=1，version=1..1000000，能力名为 `cap.learned.text.` 后接小写字母开头、最多 48 个小写字母/数字/下划线。
- 最多 16 步：`upper`、`lower`、`trim`、`prefix`、`suffix`、固定字符串 `replace`。没有 shell、代码求值、文件访问、网络调用、动态依赖或正则表达式。
- 每步输入/输出最多 4096 UTF-8 字节。replace 在分配扩展结果前检查大小。
- 1..16 个 package 样例必须全部通过；这是确定性评估，不证明老师给出的业务语义正确。先审阅 recipe，并通过可信渠道核实 SHA256。传输 ticket 中的 hash 本身不是独立信任来源。
- 已安装版本不可换内容；升级不能隐式降级。rollback 只能选择已存储且重新校验通过的版本。失败安装不改变已激活版本。
- 包内容只在 Agent 的文件/数据库中保存。Gateway 的 relay 只短暂中继有界数据帧，不保存 package 字节，也不把它写成事件。调用时的短文本输入/输出仍会作为房间事件保存，不应包含秘密。
- Ticket 是短时 bearer 凭证，不要贴到房间、日志或 Git；通过可信渠道交付接收方。共享 Gateway token 不提供多租户身份隔离。远程运行必须使用 HTTPS/WSS 与访问控制。

## 两台机器演示

先启动 Gateway，下面使用其 HTTP(S) 基础地址。两个 Agent 均只建立出站连接，不需要相互可访问的监听端口。

Teacher 运行：

```bash
go run ./cmd/skill -mode teach -url https://gateway.example \
  -teacher teacher-a -learner learner-b \
  -file examples/skills/greeting-v1.json -ticket ./receive-ticket.json
```

Teacher 校验并评估文件，将仅含接收权限的 ticket 写入 0600 本地文件，然后等待。通过安全方式将该文件送到 Learner，并独立核实 Teacher 打印的 capability、version、sha256。默认需在两分钟内完成。

Learner 运行（SHA256 用实际核实值替换）：

```bash
go run ./cmd/skill -mode learn -url https://gateway.example \
  -teacher teacher-a -learner learner-b -ticket ./receive-ticket.json \
  -skills ./learner-skills.db -capability cap.learned.text.greeting \
  -version 1 -sha256 VERIFIED_SHA256
```

Learner 在本地临时文件接收并验证完整性，安装成功才 ACK。Teacher 未收到 ACK 时结果可能不确定，不应把超时当成未安装。检查 Learner 的 list/execute 后再决定下一步。不要在服务重启或 ticket 过期后复用 ticket。

停止 Teacher、关闭 Gateway，重启进程离线执行：

```bash
go run ./cmd/skill -mode execute -skills ./learner-skills.db \
  -capability cap.learned.text.greeting -text ' GOOSE '
# {"text":"hello goose"}
```

通过原真实调用生命周期使用新能力：

```bash
go run ./cmd/agent -mode worker -id learner-b -room demo \
  -state ./learner-executions.db -skills ./learner-skills.db

go run ./cmd/invoke -room demo -id caller -target learner-b \
  -request-id greeting-001 -capability cap.learned.text.greeting -text ' GOOSE '
```

每次安装或 rollback 后重启 worker，以更新能力声明；保留同一执行日志。不要并行运行同 ID 的 worker。已有调用 ID 继续返回原持久结果，不会因为 skill 升级重新执行。

## 本地管理与回滚

```bash
go run ./cmd/skill -mode list -skills ./learner-skills.db
go run ./cmd/skill -mode install -skills ./learner-skills.db \
  -file examples/skills/greeting-v1.json -capability cap.learned.text.greeting \
  -version 1 -sha256 VERIFIED_SHA256
go run ./cmd/skill -mode rollback -skills ./learner-skills.db \
  -capability cap.learned.text.greeting -version 1
```

升级包使用同能力名和更大 version。数据库属于本机受信任操作系统边界；摘要检查用于发现损坏，不抵抗有数据库写权限的人同时替换内容与摘要。备份 SQLite 时需保留一致的 WAL 数据。

P1 HTTP Teach Session / Evaluator 接口依然只是元数据，不会安装此运行时 package。真正闭环通过上述 CLI、`internal/skills` 和流式 relay 实现。

## 验收

`go test ./internal/skills ./internal/agent ./internal/relay` 覆盖真实网络传递、分离的 Agent 磁盘、Teacher/relay 断开后的重开执行、日志去重、版本回滚、未知操作、错误摘要、版本冲突、失败评估与输入/输出边界。
