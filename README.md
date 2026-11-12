# GooseCab / ���θ�

GooseCab ��һ�����¼�Ϊ���ĵĶ� Agent ����������ѧϰ���硣�����������������Ŀ��ӻ����������档Agent ֻ�� Gateway ������վ WebSocket ���ӣ��ļ����ݱ����� Agent ���أ�Gateway ֻ�����¼��ʹ���Ԫ���ݡ�

## ��ǰ״̬

- **P0**��WebSocket ʵʱ�����桢SQLite WAL + ������ EventBus��Agent Capability Registry���������úͱ���/Relay �ļ�����Ԫ���ݡ�
- **P1**������ Teach Session/Evaluator Ԫ���� API��������ʵ����ʽ�ı��������Ĵ��ݡ����������ذ�װ�����߸�����汾�ع����� [����ѧϰ](docs/capability-learning.md)��
- **��ʵ�ı�����**����ʽ worker ִ�� `cap.text.uppercase`������ ID �ᴩִ���������־�ȥ�أ����߲��Զ����ܡ��� [��ʵ Agent ����](docs/agent-tasks.md)��
- **P2**�������滮Ԫ���� API������ Agent ����ʽ�ı� DAG �������ȡ�����������š��־÷���/״̬���˹�ֻ���ָ����� [�������](docs/task-orchestration.md)�����Զ��滮���� Agent �����Բ�ȷ������
- **��ʵ����Э��**����վ WebSocket ��ʽ�ļ� Relay��Ĭ����� 256 MiB�������ã����Լ����� Gateway ��� Leader/�����ռ� flat ������������ [��ʽ����](docs/streaming-relay.md)��[���ؼ���](docs/cascade.md)��

������� `python3 tests/collaboration-smoke.py` �������������� Gateway ���̡���� worker/bridge/CLI ���̣���֤�������ݡ���ʦ�˳����á������� DAG �� 32 MiB �ļ������ԣ�����������ڴ����������޾ܾ������Զ���ʹ�õ��� TCP �ػ�����ð���������̨�������������硢TLS �ͷ���ǽ���ա�

## һ���������ı���Skill��MCP ��֪ʶ

����Ŀ¼���� `kind`��`callable` �Ϳ��������ͻ�������`GET /api/capabilities?kind=skill`���� `mcp`��`knowledge`����ɸѡ�������� ID �������Ѱ�װ������Ȩ��ɵ��á�

- **Skill**����ʵ `manifest.json` + `SKILL.md` + �ļ� ZIP ������ͨ�� Relay ���ݡ����ս��ݴ棬�˶� SHA256 ��������׼��ż����ͨ˵��/�زİ����ֲ���ִ�У���ʽ text-recipe adapter ��������������ʱִ�С��� [Skill ��](docs/skill-bundles.md)��
- **MCP**�����ݵ�����ƾ�ݵķ�����/�����嵥�����պ��Խ��á����뱾����׼��ȷ�嵥�����÷����ѡ���䣬���ڱ�����ƾ�ݣ��Ž�����ʵ HTTP MCP ���á������Զ����� stdio ���̡���װ������ִ�в����Ŵ��롣�� [MCP ����](docs/mcp-capabilities.md)��
- **֪ʶ�����䡢����**�������־ü�¼��Ĭ��˽�У�ֻ����ȷѡ��ļ�¼�������������շ���������¼ͨ�� Relay �ļ����䣬���ĺ�ժҪ���룬������Դ�Ͱ汾�����벻���Զ��ٷ�����Զ������ֻ�������ޱ���/����Ԫ���ݣ����������ġ�����ģ��Ȩ��ѵ������ [֪ʶ����](docs/knowledge-sharing.md)��

����Զ����������������Ѱ�������Ȩ�޻�֪ʶ�Զ�װ�����÷���`cmd/call` �ṩ��ʽ JSON ���ã�worker �� `-skill-bundles`��`-mcp-store`��`-knowledge-store` ֻ���ظ��Ա�������׼״̬��ԭ `-skills` / `cap.learned.text.*` ���ݱ�����`cap.skill.*` ʹ������ Skill bundle provider����� [����Ŀ¼�����](docs/capability-catalog.md)��

���� DAG ���������ı�����/�������ܣ��ɵ����ı��� Skill��MCP ��֪ʶ�Ľṹ������ͨ�� `cmd/call`��Ҳ�ɰ���ȷ·�ɿ� Gateway������������Ȩ���� bridge �Լ��ĵ������ݣ����Զ��̳����ε����ߵ�Ȩ�ޡ�

`python3 tests/providers-smoke.py` ��֤ Skill/MCP/֪ʶ�ļ����ݡ���������ʵ���á�˽����Ϣ�߽硢ȥ�ػָ���˫���ش�����MCP ��ǰ֧�� Streamable HTTP �� JSON ��Ӧ�Ӽ�����֧�� SSE��stdio����Դ/ͼƬ��������� OAuth ���̡���С���ò������ı��������뷿����ʷ�������������ļ����Ļ����ܡ�

## ���ٿ�ʼ

Ҫ�� Go 1.23 ����°汾��

```bash
git clone https://github.com/jiejhong/GooseCab.git
cd GooseCab
go mod download
mkdir -p .data
GOOSECAB_DB="$PWD/.data/goosecab.db" go run ./cmd/server
```

Gateway Ĭ�ϼ��� `:8312`������ `-addr` �� `GOOSECAB_ADDR` ���ǣ�����������ֻ������۲�ҳ�ֱ��ǣ�

```bash
curl -fsS http://127.0.0.1:8312/healthz
# ��ǰ������ַ�����ݿ�·���� token �Ƿ�����
curl -fsS http://127.0.0.1:8312/api/status
# ������� http://127.0.0.1:8312/?room_id=demo
```

ֹͣ�����SQLite WAL ���ݿ��Իᱣ���� `.data/goosecab.db`��Ҳ������ `go build -o bin/goosecab-server ./cmd/server` �������������ơ�

## ������������ Agent

�ֿ����һ������ Agent �ͻ��ˣ�֧�� `-mode read-only`��Ĭ�ϣ��� `-mode interactive`������ģʽ���ᷢ�� `agent.hello`���Լ� hello �е� capability �б�����������ģʽֻ��ӡ/����Э���¼�����ִ������������ļ��ֽڡ���ʵִ�б�����ʽ���� `-mode worker` ���ṩ�־���־ `-state`����ѡ `-skills` ���ر�������֤���������� [���а�ȫ�ı�����](docs/agent-tasks.md)��

�ȱ�������� Gateway ���У��ٿ������նˣ�

```bash
# �ն� A������ cap.echo
go run ./cmd/agent \
  -url ws://127.0.0.1:8312/api/v1/agent/connect \
  -room demo -id agent-a -capabilities cap.echo

# �ն� B��ͬһ����ĵڶ��� Agent
go run ./cmd/agent \
  -url ws://127.0.0.1:8312/api/v1/agent/connect \
  -room demo -id agent-b
```

���ն˻ῴ������ `agent.hello` �� `capability.announce` ���¼���������˫ Agent ���ü������ļ�������ع���Կ�ֱ�����У�

```bash
go test ./internal/agent -run TestTwoAgentsAndLocalDataPlane -v
```

����ģʽ�� stdin ��ȡһ��һ�����ͬʱ���յ����¼������� progress/result����ӡΪ JSON��

```text
chat hello from agent-a
event note {"text":"local metadata"}
invoke req-1 cap.echo agent-b {"text":"hello"}
quit
```

����ʾ����

```bash
go run ./cmd/agent -mode interactive \
  -url ws://127.0.0.1:8312/api/v1/agent/connect \
  -room demo -id agent-a -capabilities cap.echo
```

`event` �� payload �� `invoke` �� payload ������ JSON����Ԫ���������� `invoke` target agent ��ʡ�ԣ�`cap.text.uppercase` ����ָ��Ŀ�ꡣ

## ����

| �������� | Ĭ��ֵ | ˵�� |
| --- | --- | --- |
| `GOOSECAB_DB` | `goosecab.db` | SQLite ���ݿ�·������������Ӧʹ�ó־û����̡� |
| `GOOSECAB_RELAY_MAX_BYTES` | `268435456`��256 MiB�� | ���ļ����ޣ��������ֽ��Ҳ�����Ĭ��ֵ������ `33554432` Ϊ 32 MiB�� |
| `GOOSECAB_ADDR` | `:8312` | HTTP���۲�ҳ�� Agent WebSocket ���õļ�����ַ��Ҳ���� `cmd/server -addr`�� |
| `GOOSECAB_AGENT_TOKEN` | �� | �ǿ�ʱ������ `/api` �ӿڣ���״̬��Registry��P1/P2�����䡢��ʷ�� HTML �������� `/ws` Ҫ�� `Authorization: Bearer <token>`�� |
| `GOOSECAB_ALLOW_CROSS_ORIGIN` | `0` | ��Ϊ `1` ��������ͬԴ����� WebSocket��ֻӦ�ڷ������������Դ����ʱ���á� |

`/` ҳ�����ʾֻ�����ú�״̬������ HTTP API���۲� WebSocket��Agent WebSocket ����ͬһ���˿ڣ����ṩ���߸����á����𵽹���ʱ����ǰ����� TLS ��ֹ�ͷ��ʿ��Ʒ����������Ҫ������ WebSocket ֱ�ӱ�¶���������ɸ��� `examples/goosecab.env.example` ��Ϊ����������㡣

���� token �󣬿��� Agent ͨ�� `-token` ��������

```bash
GOOSECAB_AGENT_TOKEN="replace-with-a-long-random-value" \
  go run ./cmd/server

go run ./cmd/agent -url ws://127.0.0.1:8312/api/v1/agent/connect \
  -room demo -id agent-a -token "replace-with-a-long-random-value"
```

�۲�ҳ֧������������ƣ�ͨ�� HTTP Authorization header ��ȡ������¼������ HTML�����ƽ������ڵ�ǰҳ���ڴ��У���д�� URL ��������洢���۲�ҳÿ 5 ���ȡ�����־û��¼������ɼ���ǩҳ�ҵ�ǰ��ҳ�Ѽ������ʱ����Agent ��������ʹ�� WebSocket��

## Bug �޸���ļ�����

- δ���� token ʱ��֧��ԭ���ı��ؿ������ã����� token ������ API ���󶼱���� Bearer header���ɵ�����֤ HTTP �ű��貹�� header��`/` �۲�ҳ����� `/healthz` �Թ���������������Ҫ��֤��
- `/ws` ��ֻ���۲�ͨ���������¼�������� `/api/v1/agent/connect`���ȷ��ͷǿ� `agent_id` �� `agent.hello`�����ӵ� `room_id` ����ǿա�ͬһ���Ӳ����ٴ��л� Agent ID��
- `event.publish` δ�� room/actor ʱ�ɷ��������룻��ʽ��д�������Ӳ�һ��ʱ���ش��󣬲�д�⡣`agent.hello`��`agent.welcome`��`capability.announce` �Լ� `capability.invoke/progress/result` ����ͨ��ͨ���¼��ƹ�ר�ô�����
- ���Զ��¼� ID �ǲ�͸�� UUID������ʹ�� `type-seq`��������ԭ�ӷ��� `seq`�������� ID�����м�¼�͵�������ʽ ID���ͻ��˲�Ҫ�� ID �Ƶ����ͻ���š�
- Registry ��ȫ���ء�����������Ŀ¼��hello ��Ǽ� Agent��announce �ϲ����������� hello �滻�� Agent ���������ϣ��������ڼ����ڵ�������ϸ��Ϣ�����Ͽ���ɾ��Ŀ¼��Ŀ��������������ע�᣻Ŀ¼����������״̬��Agent ID Ӧ��������Ψһ��
- ���� token ����ÿ�� Agent ������ƾ֤�򷿼� ACL������ token ���Կ�ѡ�����ӷ��䡢�״����� Agent ID���Լ����ù��� API�����ʺϲ����ŵĶ��⻧����

## ���������

�����ֻ��Ҫ Gateway ���ڻ����ɱ� Agent ���ʣ�Agent ����Ҫ������վ�˿ڡ��������̣�

1. �� Gateway �����Ϲ��������� `bin/goosecab-server`�������ݿ���ڳ־û����̡�
2. ��������� `wss://gateway.example.com/api/v1/agent/connect` �� `/ws` ת���� `127.0.0.1:8312`�������� TLS����Դ���ƺ�������/���ӳ�ʱ��
3. ����ǽֻ���з�������˿ڡ�Agent ����ֻ��������վ TCP ���ӵ�������
4. ��ÿ̨ Agent ���������� `cmd/agent` �򼯳� `internal/agent.Client`���� `-url` ���ɴ����� `wss://` ��ַ����������ͬ�� `-room`��������� token������ Agent ������� `-token`��

�������Ĳ������嵥��WebSocket ֡�� HTTP Ԫ����ʾ���� [`docs/deployment.md`](docs/deployment.md) �� [`docs/protocol.md`](docs/protocol.md)��

## ˫�����뼶������

ͬһ̨���Կ���ͬʱ���б��� Gateway ������Զ�� Gateway �� Agent �ͻ��ˣ�Gateway ʹ���Լ������ݿ�Ŀ¼�� `:8312`�����Զ���˿ڣ����ͻ���ֻ�г�վ WebSocket���������˿ڡ����磺

```bash
GOOSECAB_DB="$PWD/.data/local.db" GOOSECAB_ADDR=:8312 go run ./cmd/server
go run ./cmd/agent -mode interactive \
  -url wss://remote.example.com/api/v1/agent/connect \
  -room demo -id local-leader -capabilities bridge.chat
```

���ͬ����Ҫ�����ڶ��� Gateway������ʹ�ò�ͬ�� `GOOSECAB_ADDR` �� `GOOSECAB_DB`������ `:8313` �� `.data/secondary.db`����

��ǰ�ṩ��ȷ���õ� `cmd/cascade`��ʹ������ Gateway �ĳ�վ Agent ����ʵ�֣�

- **leader**��һ�����δ������ݰ�����·�ɵ�ָ������ worker��
- **flat**������ worker �� `bridge-id/worker-id` �Ķ����������ݳ��������Σ���α��ԭ worker ���ݣ�Ҳ���Զ�����Ŀ¼��

ֱ������Զ�� Gateway ����ͨ Agent �����������������ƽ̹���������ֽ����߶��� Relay�������뼶���¼�����������ԭ���������·�ɹ켣��־�ȥ�أ�δ֪�����Զ��ط������������á�˫�������պ����Ƽ� [���ؼ���](docs/cascade.md)��

## API ����

### P0

- `POST /api/agents`��`GET /api/agents`��ע��Ͳ鿴 Agent Registry Ԫ���ݡ�Agent WebSocket �� hello/announce Ҳ�����ͬһ�� Registry��
- `GET /api/capabilities?q=`���� capability ID �����Ʋ�ѯ��ѡ Agent��
- `GET /?room_id=<id>`������ʽ�۲�ҳ��չʾ���졢�������á����ȡ���������۵������� JSON��
- `GET /api/events?room_id=<id>`���������ҳ��ȡ�����¼����ա�
- `GET /api/events/export.html?room_id=<id>`������Ư�����԰��� HTML �����¼�����߿ɶ����ɴ�ӡ��
- `/ws?room_id=<id>`��ֻ��ԭʼ�۲� WebSocket�����Զ���ͻ���ʹ�ã�����д��֡���ᱻ�ܾ���
- `/api/v1/agent/connect?room_id=<id>`��Agent ��վ������ WebSocket��
- `POST /api/transfers`��`POST /api/transfers/{file_id}`���ɴ���Ԫ���� API���������ֽڴ��䡣
- `POST /api/v1/relay`��`GET /api/v1/relay/connect`��������ʱ����ƾ֤����Գ�վ��������Gateway ���н���ʽת�������������ļ��ֽڣ����� Agent У�鲢�������̡�

### P1

- `POST /api/invocations`��`POST /api/invocations/{id}/progress`��`POST /api/invocations/{id}/result`����¼�����������ںͱ��� `input_ref`/`output_ref`��
- `POST /api/teach-sessions`��������ʦ Agent ��ѧϰ�� Agent �Ľ�ѧ�Ự��
- `POST /api/skills`��`POST /api/skills/{id}/rollback`������ Skill Package manifest ����¼�汾�ع�Ԫ���ݡ�
- `POST /api/evaluations`����¼ evaluator/check ״̬�������

### P2

- `POST /api/plans`��`POST /api/tasks`��`POST /api/tasks/{id}/retry`�������ƻ���DAG ��������/�� Agent��
- `GET /api/capability-resolve?q=`���� Registry ���Һ�ѡ Agent��
- `POST /api/blackboards`��`POST /api/private-sessions`�����������ڰ��˽�лỰԪ���ݡ�

P1/P2 HTTP API ����Ҫ��¼Ԫ���ݺ��¼��������Զ�������ʵ Agent����ʵ����/ѧϰ/��������ʽ CLI ��������Ӧ�� HTTP Ԫ������Ӧ��Ϊִ�гɹ����������ֶκ��¼�֡ʾ����Э���ĵ���

## �鿴�͵���������¼

������� `http://127.0.0.1:8312/?room_id=demo`��ѡ�񷿼䲢��� **���� HTML**�����ز������Ѿ����ص���ҳ��ҳ�����Ƕ�ȡ����ʱ�÷�����������ݿ���ա��ĵ�������������У��������ߡ�UTC ʱ�䡢���ù��� ID����Ϣ���ݺ��¼���Ƭ��ԭʼ�ֶ������� JSON �۵�չʾ��

```bash
# ���� token ʱ���� -H "Authorization: Bearer $GOOSECAB_AGENT_TOKEN"
curl -fS 'http://127.0.0.1:8312/api/events?room_id=demo&limit=200'
curl -fS 'http://127.0.0.1:8312/api/events/export.html?room_id=demo' -o goosecab-chat.html
```

HTML �������Ķ�����������ԭʼ���ݣ�ƾ���ֶΡ��ļ����úͿ�ʶ�𱾵�·���ᱻ�ڸǣ�������� Agent ���ļ��ֽڣ�Ҳ�������������û�������ơ��Զ������������������ܼ����������ǰ���踴�������ı���������� 10,000 �������޷��� 413��������͵͵�ضϣ����� `after_seq` / `through_seq` �ֶε�����������ҳ�����պ���֤�߽�� [Э���ĵ�](docs/protocol.md#������¼�б���-html-����)��

## �������

```bash
gofmt -w ./cmd ./internal
go vet ./...
go test ./...
go test -race ./...
node tests/observer.test.cjs # UI ��Ϊ��飨Node.js 18+��
python3 tests/collaboration-smoke.py # ��ʵ���������� TCP ����������գ�Python 3��
python3 tests/providers-smoke.py # Skill/MCP/֪ʶ����ʵ�˵����밲ȫ�߽�
```

CI �� Linux + Go 1.23 ��ִ������������`go vet ./...`��`go test ./...`��`go test -race ./...` ���������� Node UI ��Ϊ���� Python ������������������ա�

## Native AI host chat tools

The restricted [stdio MCP chat adapter](docs/chat-mcp.md) lets a Codex, Claude Code, or OpenCode host call fixed-room chat/history/discovery tools itself. This is separate from the capability-provider MCP client. See the document for host configuration, shared-token boundaries, reliability limits, and validation scope.
