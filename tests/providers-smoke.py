#!/usr/bin/env python3
"""Real CLI/Gateway/HTTP acceptance for Skill, MCP and selected knowledge sharing."""
import hashlib
import http.server
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import threading
import time
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
GATEWAY_TOKEN = "provider-smoke-gateway-token"
MCP_TOKEN = "provider-smoke-secret-never-export"
ENV = dict(os.environ, GOOSECAB_AGENT_TOKEN=GATEWAY_TOKEN, TEST_MCP_TOKEN=MCP_TOKEN, GOOSECAB_UPSTREAM_TOKEN=GATEWAY_TOKEN, GOOSECAB_DOWNSTREAM_TOKEN=GATEWAY_TOKEN)
processes, logs = [], []


def free_port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def write(path, value):
    path.write_text(json.dumps(value), encoding="utf8")


def run(cmd, *args, ok=True):
    p = subprocess.run([str(BIN / cmd), *map(str, args)], env=ENV, capture_output=True, text=True, timeout=20)
    if ok and p.returncode != 0:
        raise RuntimeError(f"{cmd}: {p.stderr}\n{p.stdout}")
    return p


def start(label, cmd, *args, env=None):
    log = open(TMP / (label + ".log"), "w+")
    logs.append((label, log))
    p = subprocess.Popen([str(BIN / cmd), *map(str, args)], env=env or ENV, stdout=log, stderr=log)
    processes.append(p)
    return p


def stop(p):
    if p.poll() is None:
        p.terminate()
        try:
            p.wait(timeout=5)
        except subprocess.TimeoutExpired:
            p.kill()
            p.wait(timeout=5)


def wait(check):
    end = time.monotonic() + 10
    while time.monotonic() < end:
        try:
            if check(): return
        except (ValueError, OSError): pass
        time.sleep(.05)
    raise RuntimeError("readiness timeout")


def api(path, base=None):
    req = urllib.request.Request((base or URL) + path, headers={"Authorization": "Bearer " + GATEWAY_TOKEN})
    with urllib.request.urlopen(req, timeout=2) as r: return json.load(r)


def relay_file(src, dest, stem):
    sender_ticket, receiver_ticket = TMP / (stem + "-send.json"), TMP / (stem + "-recv.json")
    run("transfer", "-mode", "create", "-url", URL, "-ticket", sender_ticket,
        "-source", src, "-sender", "teacher", "-recipient", "learner")
    ticket = json.loads(sender_ticket.read_text())
    ticket.pop("send_secret")
    write(receiver_ticket, ticket)
    receiver = start(stem, "transfer", "-mode", "receive", "-url", URL,
                     "-ticket", receiver_ticket, "-dir", dest.parent, "-name", dest.name)
    run("transfer", "-mode", "send", "-url", URL, "-ticket", sender_ticket, "-source", src)
    assert receiver.wait(timeout=10) == 0
    assert hashlib.sha256(dest.read_bytes()).digest() == hashlib.sha256(src.read_bytes()).digest()


class MCPServer(http.server.BaseHTTPRequestHandler):
    calls = 0
    def log_message(self, *args): pass
    def do_POST(self):
        assert self.headers.get("Authorization") == "Bearer " + MCP_TOKEN
        req = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        method = req["method"]
        if method == "notifications/initialized":
            self.send_response(202); self.end_headers(); return
        if method == "initialize":
            result = {"protocolVersion": "2025-03-26", "capabilities": {"tools": {}}, "serverInfo": {"name": "fixture", "version": "1"}}
        elif method == "tools/list":
            result = {"tools": [{"name": "echo", "inputSchema": SCHEMA}]}
        elif method == "tools/call":
            assert req["params"]["name"] == "echo"
            MCPServer.calls += 1
            result = {"content": [{"type": "text", "text": "mcp:" + req["params"]["arguments"]["text"]}]}
        else:
            raise AssertionError(method)
        body = json.dumps({"jsonrpc": "2.0", "id": req["id"], "result": result}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers(); self.wfile.write(body)


SCHEMA = {"type": "object", "properties": {"text": {"type": "string"}}, "required": ["text"], "additionalProperties": False}
with tempfile.TemporaryDirectory(prefix="goosecab-providers-") as tmp:
    TMP = Path(tmp); BIN = TMP / "bin"; BIN.mkdir()
    for command in ("server", "agent", "call", "transfer", "skillbundle", "mcp", "knowledge", "cascade"):
        subprocess.run(["go", "build", "-o", str(BIN / command), "./cmd/" + command], cwd=ROOT, check=True)
    mcp = http.server.ThreadingHTTPServer(("127.0.0.1", 0), MCPServer)
    threading.Thread(target=mcp.serve_forever, daemon=True).start()
    try:
        URL = f"http://127.0.0.1:{free_port()}"; WS = URL.replace("http", "ws") + "/api/v1/agent/connect"
        db = TMP / "gateway.db"
        gateway = start("gateway", "server", "-addr", URL.split("//")[1], env=dict(ENV, GOOSECAB_DB=str(db)))
        wait(lambda: api("/healthz")["ok"])
        teacher, learner = TMP / "teacher", TMP / "learner"; teacher.mkdir(); learner.mkdir()
        source_zip, received_zip, bundle_root = teacher / "greeting.zip", learner / "greeting.zip", learner / "bundles"
        packed = json.loads(run("skillbundle", "-mode", "pack", "-dir", ROOT / "examples/skill-bundle-greeting", "-file", source_zip).stdout)
        relay_file(source_zip, received_zip, "skill-transfer")
        run("skillbundle", "-mode", "stage", "-root", bundle_root, "-file", received_zip)
        assert json.loads(run("skillbundle", "-mode", "list", "-root", bundle_root).stdout) in (None, [])
        run("skillbundle", "-mode", "approve", "-root", bundle_root, "-sha256", packed["sha256"], "-capability", "cap.skill.greeting", "-version", "1")
        manifest = teacher / "mcp.json"
        write(manifest, {"format": 1, "name": "fixture", "endpoint": f"http://127.0.0.1:{mcp.server_port}/mcp",
                         "tools": [{"capability": "cap.mcp.echo", "name": "echo", "input_schema": SCHEMA}]})
        received_manifest = learner / "mcp.json"
        relay_file(manifest, received_manifest, "mcp-transfer")
        mcpdb = learner / "mcp.db"
        digest = run("mcp", "-state", mcpdb, "-file", received_manifest, "import").stdout.strip()
        assert not json.loads(run("mcp", "-state", mcpdb, "list").stdout)[0]["approved"]
        run("mcp", "-state", mcpdb, "-sha256", digest, "-callers", "learner,edge/bridge", "-rooms", "providers",
            "-allow-http-loopback", "-token-env", "TEST_MCP_TOKEN", "approve")
        exported = run("mcp", "-state", mcpdb, "-sha256", digest, "export").stdout
        assert MCP_TOKEN not in exported and "TEST_MCP_TOKEN" not in exported and "AllowedCallers" not in exported
        ownerdb, learnerdb = teacher / "knowledge.db", learner / "knowledge.db"
        refs = []
        for name, title, body in (("shared", "Shared lesson", "SHARED_KNOWLEDGE_BODY_CANARY"), ("private", "Private lesson", "PRIVATE_KNOWLEDGE_BODY_CANARY")):
            record = teacher / (name + ".json")
            write(record, {"id": name, "version": 1, "title": title, "body": body, "source": "/private/source/" + name})
            refs.append(json.loads(run("knowledge", "-mode", "put", "-knowledge", ownerdb, "-id", "owner", "-file", record).stdout))
        collection = teacher / "share.json"; write(collection, {"name": "lessons", "records": [refs[0]], "callers": ["learner", "edge/bridge"]})
        run("knowledge", "-mode", "share", "-knowledge", ownerdb, "-id", "owner", "-file", collection)
        ticket = teacher / "knowledge-ticket.json"
        sender = start("knowledge-sender", "knowledge", "-mode", "send", "-knowledge", ownerdb, "-id", "owner",
                       "-collection", "lessons", "-recipient", "learner", "-ticket", ticket, "-url", URL)
        wait(lambda: ticket.exists() and ticket.stat().st_size > 0)
        # Only the receiver ticket crosses out of band; records cross the relay.
        receive_ticket = learner / "knowledge-ticket.json"; receive_ticket.write_bytes(ticket.read_bytes())
        received = Path(run("knowledge", "-mode", "receive", "-id", "learner", "-ticket", receive_ticket,
                            "-stage-dir", learner, "-url", URL).stdout.strip())
        assert sender.wait(timeout=10) == 0
        assert json.loads(run("knowledge", "-mode", "search", "-knowledge", learnerdb, "-id", "learner").stdout) in (None, [])
        review = json.loads(run("knowledge", "-mode", "review", "-file", received).stdout)
        run("knowledge", "-mode", "import", "-knowledge", learnerdb, "-id", "learner", "-file", received, "-approve-sha256", review["sha256"])
        learned = json.loads(run("knowledge", "-mode", "search", "-knowledge", learnerdb, "-id", "learner").stdout)
        assert len(learned) == 1 and learned[0]["body"] == "SHARED_KNOWLEDGE_BODY_CANARY" and learned[0]["origin"] == "owner"
        def start_worker(label):
            return start(label, "agent", "-mode", "worker", "-id", "owner", "-room", "providers", "-url", WS,
                         "-state", teacher / "worker.db", "-skill-bundles", bundle_root, "-mcp-store", mcpdb, "-knowledge-store", ownerdb)
        worker = start_worker("worker")
        def catalog_ready():
            data = api("/api/capabilities")
            return any(a["id"] == "owner" and {c.get("kind") for c in a["capabilities"] if c.get("callable")} >= {"skill", "mcp", "knowledge"} for a in data)
        wait(catalog_ready)
        catalog = json.dumps(api("/api/capabilities"))
        assert MCP_TOKEN not in catalog and str(TMP) not in catalog and "http://" not in catalog
        def call(cap, request, payload, caller="learner", ok=True, endpoint=None, target="owner"):
            p = run("call", "-url", endpoint or WS, "-room", "providers", "-id", caller, "-target", target,
                    "-capability", cap, "-request-id", request, "-json", json.dumps(payload), ok=ok)
            return p, json.loads(p.stdout)
        assert call("cap.skill.greeting", "skill-1", {"text": " HELLO "})[1]["output"]["text"] == "hello!"
        assert call("cap.mcp.echo", "mcp-1", {"text": "hello"})[1]["output"]["text"] == "mcp:hello"
        assert MCPServer.calls == 1
        denied, result = call("cap.mcp.echo", "mcp-denied", {"text": "no"}, caller="intruder", ok=False)
        assert denied.returncode != 0 and result["status"] == "failed" and MCPServer.calls == 1
        hits = json.loads(call("cap.knowledge.lessons", "knowledge-1", {"query": "lesson"})[1]["output"]["text"])
        assert len(hits["hits"]) == 1 and hits["hits"][0]["title"] == "Shared lesson"
        denied, result = call("cap.knowledge.lessons", "knowledge-denied", {"query": ""}, caller="intruder", ok=False)
        assert denied.returncode != 0 and result["status"] == "failed"
        stop(worker); worker = start_worker("worker-restart")
        assert call("cap.mcp.echo", "mcp-1", {"text": "hello"})[1]["output"]["text"] == "mcp:hello"
        assert MCPServer.calls == 1
        # Fresh call is the restart readiness barrier; old request must remain one execution.
        assert call("cap.mcp.echo", "mcp-2", {"text": "ready"})[1]["output"]["text"] == "mcp:ready"
        assert MCPServer.calls == 2
        # Explicit bridge routes also carry rich metadata. The downstream MCP
        # and knowledge stores separately approve the bridge's actual caller ID.
        upurl = f"http://127.0.0.1:{free_port()}"; upws = upurl.replace("http", "ws") + "/api/v1/agent/connect"
        start("up-gateway", "server", "-addr", upurl.split("//")[1], env=dict(ENV, GOOSECAB_DB=str(TMP / "up-gateway.db")))
        wait(lambda: api("/healthz", upurl)["ok"])
        bridge_config = TMP / "bridge.json"
        write(bridge_config, {"id": "edge", "mode": "leader", "upstream": {"url": upws, "room": "providers"},
                              "downstream": {"url": WS, "room": "providers"}, "max_hops": 4,
                              "routes": [{"worker": "owner", "capability": c} for c in ("cap.skill.greeting", "cap.mcp.echo", "cap.knowledge.lessons")]})
        start("bridge", "cascade", "-config", bridge_config, "-state", TMP / "bridge.db")
        wait(lambda: any(a["id"] == "edge" and len([c for c in a["capabilities"] if c.get("callable")]) == 3 for a in api("/api/capabilities", upurl)))
        assert call("cap.skill.greeting", "skill-bridge", {"text": " BRIDGE "}, endpoint=upws, target="edge")[1]["output"]["text"] == "bridge!"
        assert call("cap.mcp.echo", "mcp-bridge", {"text": "bridge"}, endpoint=upws, target="edge")[1]["output"]["text"] == "mcp:bridge"
        assert MCPServer.calls == 3
        assert len(json.loads(call("cap.knowledge.lessons", "knowledge-bridge", {"query": "lesson"}, endpoint=upws, target="edge")[1]["output"]["text"])["hits"]) == 1
        for path in TMP.glob("*gateway.db*"):
            raw = path.read_bytes()
            for private in (MCP_TOKEN, "SHARED_KNOWLEDGE_BODY_CANARY", "PRIVATE_KNOWLEDGE_BODY_CANARY", "/private/source/"):
                assert private.encode() not in raw, (path, private)
        print("PASS: Skill file relay/stage/approval/remote execution; inert MCP manifest relay/local approval/real HTTP call/authorization/durable no-rerun; selected knowledge relay/review/import/offline provenance search and metadata-only authorized remote search; rich typed descriptors and all three provider calls across two real Gateways; no credential or full knowledge bodies persisted by Gateway.")
        print("Scope: local HTTP MCP JSON response subset, no stdio/SSE/resource outputs or arbitrary Skill code; separate real processes on one host, not physical-host acceptance.")
    except Exception:
        for name, log in logs:
            log.flush(); log.seek(0); print("---", name, "---\n", log.read()[-5000:])
        raise
    finally:
        for p in reversed(processes): stop(p)
        mcp.shutdown(); mcp.server_close()
        for _, log in logs: log.close()
