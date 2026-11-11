#!/usr/bin/env python3
"""Real process/network acceptance; one host, distinct process-owned directories.

Requires Go 1.23+ and Python 3. No CopyLocal or embedded test-server substitute.
This validates a portable TCP protocol, not two physical hosts/TLS deployment.
"""
import hashlib
import json
import os
from pathlib import Path
import socket
import sqlite3
import subprocess
import tempfile
import time
import urllib.request
import urllib.error

ROOT = Path(__file__).resolve().parents[1]
TOKEN = "isolated-smoke-token-not-a-production-secret"
ENV = dict(os.environ, GOOSECAB_AGENT_TOKEN=TOKEN,
           GOOSECAB_UPSTREAM_TOKEN=TOKEN, GOOSECAB_DOWNSTREAM_TOKEN=TOKEN)
PROCS, LOGS = [], []
FILE_BYTES = 32 * 1024 * 1024


def port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def write(path, obj):
    path.write_text(json.dumps(obj), encoding="utf8")


def run(binary, *args, timeout=20):
    p = subprocess.run([str(BIN / binary), *map(str, args)], env=ENV,
                       cwd=ROOT, capture_output=True, text=True, timeout=timeout)
    if p.returncode:
        raise RuntimeError(f"{binary} exited {p.returncode}: {p.stderr}\n{p.stdout}")
    return p.stdout


def start(label, binary, *args, env=None, cwd=None):
    log = open(TMP / (label + ".log"), "w+")
    LOGS.append((label, log))
    p = subprocess.Popen([str(BIN / binary), *map(str, args)],
                         env=env or ENV, cwd=cwd or ROOT, stdout=log, stderr=log)
    PROCS.append(p)
    return p


def wait(check, description, timeout=10):
    end = time.monotonic() + timeout
    while time.monotonic() < end:
        try:
            if check():
                return
        except (OSError, ValueError):
            pass
        time.sleep(.05)
    raise RuntimeError("Timed out: " + description)


def api(base, path):
    req = urllib.request.Request(base + path, headers={"Authorization": "Bearer " + TOKEN})
    with urllib.request.urlopen(req, timeout=1) as r:
        return json.load(r)


def registered(base, agent):
    return any(a["id"] == agent for a in api(base, "/api/agents"))


def finish(p, label, timeout=15):
    if p.wait(timeout=timeout) != 0:
        raise RuntimeError(label + " failed")


def stop(p):
    if p.poll() is None:
        p.terminate()
        try:
            p.wait(timeout=5)
        except subprocess.TimeoutExpired:
            p.kill()
            p.wait(timeout=5)


def file_digest(path):
    h = hashlib.sha256()
    with open(path, "rb") as source:
        for chunk in iter(lambda: source.read(64 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def process_kib(pid, field):
    # Linux CI observes the gateway process itself, not the Python test runner.
    status = Path(f"/proc/{pid}/status")
    if not status.exists():
        return None
    for line in status.read_text().splitlines():
        if line.startswith(field + ":"):
            return int(line.split()[1])
    return None


def invokes(db):
    with sqlite3.connect(db) as c:
        return c.execute("select count(*) from events where type='capability.invoke'").fetchone()[0]


with tempfile.TemporaryDirectory(prefix="goosecab-processes-") as tmp:
    TMP = Path(tmp)
    BIN = TMP / "bin"
    BIN.mkdir()
    for name in ("server", "agent", "invoke", "plan", "cascade", "skill", "transfer"):
        subprocess.run(["go", "build", "-o", str(BIN / name), "./cmd/" + name], cwd=ROOT, check=True)
    try:
        UP, DOWN = f"http://127.0.0.1:{port()}", f"http://127.0.0.1:{port()}"
        dirs, gateways = {}, {}
        for name, base in (("up", UP), ("down", DOWN)):
            dirs[name] = TMP / name
            dirs[name].mkdir()
            env = dict(ENV, GOOSECAB_DB=str(dirs[name] / "gateway.db"), GOOSECAB_RELAY_MAX_BYTES=str(FILE_BYTES))
            gateways[name] = start(name, "server", "-addr", base.split("//")[1], env=env, cwd=dirs[name])
            wait(lambda b=base: api(b, "/healthz")["ok"], name + " gateway")
        for name in ("teacher", "learner", "worker-b", "caller", "bridges"):
            dirs[name] = TMP / name
            dirs[name].mkdir()
        # Teaching bytes cross HTTP+WebSockets into a different agent directory.
        cap = "cap.learned.text.greeting"
        canary = "PACKAGE_BYTES_PRIVATE_CANARY_9d390f"
        package = {"format": 1, "capability": cap, "version": 1,
                   "operations": [{"op": "trim"}, {"op": "upper"}, {"op": "prefix", "value": "HELLO "}],
                   "tests": [{"input": canary, "want": "HELLO " + canary.upper()}]}
        source = dirs["teacher"] / "recipe.json"
        write(source, package)
        digest = hashlib.sha256(source.read_bytes()).hexdigest()
        ticket = dirs["teacher"] / "receive-ticket.json"
        teach = start("teach", "skill", "-mode", "teach", "-url", DOWN, "-file", source,
                      "-ticket", ticket, "-teacher", "teacher", "-learner", "learner")
        wait(lambda: ticket.exists() and ticket.stat().st_size > 0, "teaching ticket")
        # Only the private receive-role ticket is conveyed out of band. File
        # bytes themselves may not be copied between agent directories.
        learner_ticket = dirs["learner"] / "receive-ticket.json"
        learner_ticket.write_bytes(ticket.read_bytes())
        skillsdb = dirs["learner"] / "skills.db"
        run("skill", "-mode", "learn", "-url", DOWN, "-ticket", learner_ticket,
            "-teacher", "teacher", "-learner", "learner", "-skills", skillsdb,
            "-capability", cap, "-version", "1", "-sha256", digest)
        finish(teach, "teacher transfer")
        assert json.loads(run("skill", "-mode", "execute", "-skills", skillsdb,
                              "-capability", cap, "-text", "  goose  "))["text"] == "HELLO GOOSE"
        assert teach.poll() == 0  # teacher no longer running
        # Raw file uses a separate relay session and verified receiver publication.
        blob = dirs["teacher"] / "payload.bin"
        # Generate and hash 32 MiB with bounded chunks; no shared input/output
        # files or whole-file buffers are used by either receiving process.
        marker = b"RAW_FILE_PRIVATE_CANARY_131f"
        with blob.open("wb") as f:
            f.write(marker)
            remaining = FILE_BYTES - len(marker)
            while remaining:
                chunk = os.urandom(min(64 * 1024, remaining))
                f.write(chunk)
                remaining -= len(chunk)
        source_digest = file_digest(blob)
        assert blob.stat().st_size == FILE_BYTES
        baseline_rss = process_kib(gateways["down"].pid, "VmRSS")
        oversized = json.dumps({"sender": "teacher", "recipient": "learner",
                                "size": FILE_BYTES + 1, "sha256": source_digest}).encode()
        req = urllib.request.Request(DOWN + "/api/v1/relay", data=oversized,
                                     headers={"Authorization": "Bearer " + TOKEN, "Content-Type": "application/json"})
        try:
            urllib.request.urlopen(req, timeout=3).close()
            raise AssertionError("configured relay limit accepted one excess byte")
        except urllib.error.HTTPError as error:
            assert error.code == 400, error.code
        raw_ticket = dirs["teacher"] / "file-ticket.json"
        run("transfer", "-mode", "create", "-url", DOWN, "-source", blob,
            "-ticket", raw_ticket, "-sender", "teacher", "-recipient", "learner")
        receiver_ticket = dirs["learner"] / "file-ticket.json"
        receive_only = json.loads(raw_ticket.read_text())
        receive_only.pop("send_secret", None)
        write(receiver_ticket, receive_only)
        recv = start("receive-file", "transfer", "-mode", "receive", "-url", DOWN,
                     "-ticket", receiver_ticket, "-dir", dirs["learner"], "-name", "received.bin")
        run("transfer", "-mode", "send", "-url", DOWN, "-ticket", raw_ticket, "-source", blob)
        finish(recv, "file receiver")
        received = dirs["learner"] / "received.bin"
        assert received.stat().st_size == FILE_BYTES and file_digest(received) == source_digest
        peak_rss = process_kib(gateways["down"].pid, "VmHWM")
        if baseline_rss is not None and peak_rss is not None:
            # A 32 MiB whole-file allocation would exceed this 24 MiB growth
            # allowance. This is a measured regression bound, not a total-RSS cap.
            assert peak_rss - baseline_rss < 24 * 1024, (baseline_rss, peak_rss)
            print(f"Relay 32 MiB: size={FILE_BYTES}, sha256={source_digest}, gateway baseline RSS={baseline_rss} KiB, peak RSS={peak_rss} KiB, growth={peak_rss-baseline_rss} KiB; oversized metadata rejected.")
        else:
            print(f"Relay 32 MiB: size={FILE_BYTES}, sha256={source_digest}; RSS measurement unavailable on this OS, enforce Linux CI memory regression separately.")
        wsdown, wsup = DOWN.replace("http", "ws") + "/api/v1/agent/connect", UP.replace("http", "ws") + "/api/v1/agent/connect"
        worker = start("learner-worker", "agent", "-mode", "worker", "-url", wsdown,
                       "-room", "local", "-id", "learner", "-state", dirs["learner"] / "exec.db", "-skills", skillsdb)
        start("second-worker", "agent", "-mode", "worker", "-url", wsdown,
              "-room", "local", "-id", "worker-b", "-state", dirs["worker-b"] / "exec.db")
        wait(lambda: registered(DOWN, "learner") and registered(DOWN, "worker-b"), "worker announcements")
        bridge_procs = []
        for mode in ("leader", "flat"):
            cfg = {"id": mode, "mode": mode,
                   "upstream": {"url": wsup, "room": "shared"},
                   "downstream": {"url": wsdown, "room": "local"}, "max_hops": 4,
                   "routes": [{"worker": "learner" if mode == "leader" else "worker-b", "capability": "cap.text.uppercase"}]}
            if mode == "leader":
                cfg["routes"].append({"worker": "learner", "capability": cap})
            path = dirs["bridges"] / (mode + ".json")
            write(path, cfg)
            bridge_procs.append(start(mode, "cascade", "-config", path, "-state", dirs["bridges"] / (mode + ".db")))
        wait(lambda: registered(UP, "leader") and registered(UP, "flat/worker-b"), "bridge identities")
        learned = json.loads(run("invoke", "-url", wsup, "-room", "shared", "-id", "caller",
                                "-target", "leader", "-capability", cap, "-request-id", "learned-call", "-text", "  world  "))
        assert learned["output"]["text"] == "HELLO WORLD"
        plan = {"id": "network-dag", "url": wsup, "room": "shared", "caller": "planner",
                "concurrency": 2, "separator": "|", "tasks": [
                    {"id": "a", "target": "leader", "text": "alpha"},
                    {"id": "b", "target": "flat/worker-b", "text": "beta"},
                    {"id": "join", "target": "leader", "capability": cap, "text": "joined", "depends_on": ["a", "b"]}]}
        planpath, statedb = dirs["caller"] / "plan.json", dirs["caller"] / "plan.db"
        write(planpath, plan)
        states = json.loads(run("plan", "-action", "run", "-plan", planpath, "-state", statedb))
        joined = next(x for x in states if x["id"] == "join")
        assert joined["status"] == "completed" and joined["result"]["output"]["text"] == "HELLO JOINED|ALPHA|BETA"
        before = invokes(dirs["down"] / "gateway.db")
        # Plan reopen/run and bridge/worker restart must not create new invocations.
        run("plan", "-action", "run", "-plan", planpath, "-state", statedb)
        for p in bridge_procs:
            stop(p)
        stop(worker)
        worker = start("learner-worker-restart", "agent", "-mode", "worker", "-url", wsdown,
                       "-room", "local", "-id", "learner", "-state", dirs["learner"] / "exec.db", "-skills", skillsdb)
        for mode in ("leader", "flat"):
            start(mode + "-restart", "cascade", "-config", dirs["bridges"] / (mode + ".json"), "-state", dirs["bridges"] / (mode + ".db"))
        cached = json.loads(run("invoke", "-url", wsup, "-room", "shared", "-id", "caller",
                               "-target", "leader", "-capability", cap, "-request-id", "learned-call", "-text", "  world  "))
        assert cached["output"]["text"] == "HELLO WORLD"
        # A fresh successful call through each restarted bridge is a readiness
        # barrier: cached upstream history alone would not prove replay finished.
        for mode, target in (("leader", "leader"), ("flat", "flat/worker-b")):
            probe = json.loads(run("invoke", "-url", wsup, "-room", "shared", "-id", "caller",
                                   "-target", target, "-request-id", "restart-probe-" + mode, "-text", "ready"))
            assert probe["output"]["text"] == "READY"
        assert invokes(dirs["down"] / "gateway.db") == before + 2, "restart resubmitted old work"
        for p in PROCS:
            if p not in (teach, recv, *bridge_procs) and p is not worker and p.poll() not in (None, 0):
                raise RuntimeError("unexpected child failure")
        for name in ("up", "down"):
            for path in dirs[name].iterdir():
                assert path.name.startswith("gateway.db"), "unexpected gateway data file: " + str(path)
                data = path.read_bytes()
                assert canary.encode() not in data and b"RAW_FILE_PRIVATE_CANARY_131f" not in data, "file bytes persisted on gateway"
        print("PASS: two real gateway processes, independent worker processes, leader + flat routes, learned capability after teacher exit, network DAG fan-in, restart no-resubmit, 32 MiB SHA256 file relay with bounded RSS and oversized rejection, no gateway file persistence.")
        print("Scope: loopback TCP on one host; physical-host/firewall/TLS acceptance remains a deployment check.")
    except Exception:
        for label, log in LOGS:
            log.flush()
            log.seek(0)
            print("\n---", label, "---\n", log.read()[-8000:])
        raise
    finally:
        for p in reversed(PROCS):
            stop(p)
        for _, log in LOGS:
            log.close()
