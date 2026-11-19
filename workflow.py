"""Management-plane state for workspace configuration and SOP execution."""
import json
import re
import secrets
import time
from uuid import uuid4

from delivery import ENGINES, Platform, validated_review

SCHEMA = """
CREATE TABLE IF NOT EXISTS resources (
    id TEXT PRIMARY KEY, workspace TEXT NOT NULL REFERENCES workspaces,
    kind TEXT NOT NULL, name TEXT NOT NULL, config TEXT NOT NULL,
    UNIQUE(workspace, kind, name)
);
CREATE TABLE IF NOT EXISTS sops (
    id TEXT PRIMARY KEY, workspace TEXT NOT NULL REFERENCES workspaces,
    name TEXT NOT NULL, nodes TEXT NOT NULL, UNIQUE(workspace, name)
);
CREATE TABLE IF NOT EXISTS executors (
    id TEXT PRIMARY KEY, name TEXT NOT NULL, proof TEXT NOT NULL,
    seen REAL NOT NULL, enabled INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE IF NOT EXISTS runs (
    requirement TEXT PRIMARY KEY REFERENCES requirements,
    snapshot TEXT NOT NULL, executor TEXT REFERENCES executors
);
CREATE TABLE IF NOT EXISTS steps (
    requirement TEXT NOT NULL REFERENCES runs, node TEXT NOT NULL, status TEXT NOT NULL,
    lease TEXT UNIQUE, artifact TEXT, PRIMARY KEY(requirement, node)
);
CREATE TABLE IF NOT EXISTS drafts (
    id TEXT PRIMARY KEY, workspace TEXT NOT NULL REFERENCES workspaces,
    engine TEXT NOT NULL, messages TEXT NOT NULL, status TEXT NOT NULL,
    executor TEXT REFERENCES executors, lease TEXT UNIQUE, nodes TEXT, error TEXT
);
"""


def decode(row, *fields):
    result = dict(row)
    for field in fields:
        result[field] = json.loads(result[field]) if result[field] is not None else None
    return result


class WorkflowPlatform(Platform):
    def __init__(self, root):
        super().__init__(root)
        with self.db() as db:
            db.executescript(SCHEMA)

    def resources(self, workspace, db=None):
        if db is None:
            with self.db() as connection:
                return self.resources(workspace, connection)
        return [decode(row, "config") for row in db.execute(
            "SELECT * FROM resources WHERE workspace = ? ORDER BY name", (workspace,))]

    def resource(self, workspace, kind, name, config, identifier=None):
        if kind not in {"agent", "skill", "mcp"} or not name.strip() or not isinstance(config, dict):
            raise ValueError("资源类型、名称或配置无效")
        with self.db() as db:
            db.execute("BEGIN IMMEDIATE")
            if kind == "agent":
                if config.get("engine") not in ENGINES:
                    raise ValueError("请选择 Claude Code 或 Codex")
                for key, resource_kind in (("skills", "skill"), ("mcps", "mcp")):
                    keys = config.get(key, [])
                    allowed = {row[0] for row in db.execute(
                        "SELECT id FROM resources WHERE workspace = ? AND kind = ?", (workspace, resource_kind))}
                    if not isinstance(keys, list) or not set(keys) <= allowed:
                        raise ValueError("Agent 只能引用本 workspace 的资源")
            if kind == "skill" and not isinstance(config.get("content"), str):
                raise ValueError("Skill 需要文本内容")
            if kind == "mcp":
                command = config.get("command")
                args = config.get("args", [])
                if not isinstance(command, str) or not command.strip() or not isinstance(args, list) or not all(
                        isinstance(arg, str) for arg in args):
                    raise ValueError("MCP 需要 stdio command 和字符串 args")
            if identifier:
                changed = db.execute("UPDATE resources SET name = ?, config = ? WHERE id = ? AND workspace = ? AND kind = ?",
                                     (name.strip(), json.dumps(config), identifier, workspace, kind))
                if not changed.rowcount:
                    raise ValueError("资源不存在")
            else:
                identifier = uuid4().hex
                db.execute("INSERT INTO resources VALUES (?, ?, ?, ?, ?)",
                           (identifier, workspace, kind, name.strip(), json.dumps(config)))
        return identifier

    def delete_resource(self, workspace, identifier):
        with self.db() as db:
            db.execute("BEGIN IMMEDIATE")
            configs = [json.loads(row[0]) for row in db.execute(
                "SELECT config FROM resources WHERE workspace = ? AND kind = 'agent'", (workspace,))]
            nodes = [node for row in db.execute("SELECT nodes FROM sops WHERE workspace = ?", (workspace,))
                     for node in json.loads(row[0])]
            if (any(identifier in config.get("skills", []) + config.get("mcps", []) for config in configs)
                    or any(node.get("agent") == identifier for node in nodes)):
                raise ValueError("请先移除资源引用")
            if not db.execute("DELETE FROM resources WHERE workspace = ? AND id = ?",
                              (workspace, identifier)).rowcount:
                raise ValueError("资源不存在")

    def delete_sop(self, workspace, identifier):
        with self.db() as db:
            if not db.execute("DELETE FROM sops WHERE workspace = ? AND id = ?",
                              (workspace, identifier)).rowcount:
                raise ValueError("SOP 不存在")

    def validate_nodes(self, workspace, nodes, db=None):
        if not isinstance(nodes, list) or not 0 < len(nodes) <= 50:
            raise ValueError("SOP 需要 1 至 50 个节点")
        agents = {row["id"] for row in self.resources(workspace, db) if row["kind"] == "agent"}
        normal = {}
        for node in nodes:
            key = node.get("id")
            name = node.get("name", "")
            kind = node.get("type")
            depends = node.get("depends", [])
            if (not isinstance(key, str) or not re.fullmatch(r"[\w-]{1,64}", key)
                    or key in normal or not isinstance(name, str) or not name.strip()
                    or kind not in {"agent", "review"} or not isinstance(depends, list)
                    or not all(isinstance(entry, str) for entry in depends)
                    or not isinstance(node.get("prompt", ""), str)):
                raise ValueError("SOP 节点格式或编号无效")
            clean = {"id": key, "name": name.strip(), "type": kind,
                     "depends": list(dict.fromkeys(depends)), "prompt": node.get("prompt", "")}
            if kind == "agent":
                if node.get("agent") not in agents:
                    raise ValueError("Agent 节点须选择本 workspace 的 Agent")
                clean.update(agent=node["agent"], publish=bool(node.get("publish")))
            normal[key] = clean
        resolved = set()
        while len(resolved) < len(normal):
            ready = {key for key, node in normal.items() if set(node["depends"]) <= resolved}
            if ready <= resolved:
                raise ValueError("SOP 依赖存在循环或引用了不存在的节点")
            resolved.update(ready)
        return list(normal.values())

    def save_sop(self, workspace, name, nodes, identifier=None):
        if not name.strip():
            raise ValueError("请输入 SOP 名称")
        with self.db() as db:
            db.execute("BEGIN IMMEDIATE")
            nodes = self.validate_nodes(workspace, nodes, db)
            if identifier:
                if not db.execute("UPDATE sops SET name = ?, nodes = ? WHERE id = ? AND workspace = ?",
                                  (name.strip(), json.dumps(nodes), identifier, workspace)).rowcount:
                    raise ValueError("SOP 不存在")
            else:
                identifier = uuid4().hex
                db.execute("INSERT INTO sops VALUES (?, ?, ?, ?)",
                           (identifier, workspace, name.strip(), json.dumps(nodes)))
        return identifier

    def workspace_setup(self, workspace):
        with self.db() as db:
            sops = [decode(row, "nodes") for row in db.execute(
                "SELECT * FROM sops WHERE workspace = ? ORDER BY name", (workspace,))]
        return {"resources": self.resources(workspace), "sops": sops}

    def requirement(self, workspace, title, description, engine, repositories, sop=None):
        if not sop:
            return super().requirement(workspace, title, description, engine, repositories)
        with self.db() as db:
            db.execute("BEGIN IMMEDIATE")
            resources = {row["id"]: row for row in self.resources(workspace, db)}
            row = db.execute("SELECT * FROM sops WHERE workspace = ? AND id = ?", (workspace, sop)).fetchone()
            if row is None:
                raise ValueError("请选择本 workspace 的 SOP")
            snapshot = decode(row, "nodes")
            for node in snapshot["nodes"]:
                if node["type"] != "agent":
                    continue
                agent = resources[node["agent"]]
                config = agent["config"]
                node["runtime"] = {"name": agent["name"], "engine": config["engine"],
                                   "prompt": config.get("prompt", ""),
                                   "skills": [resources[key] for key in config.get("skills", [])],
                                   "mcps": [resources[key] for key in config.get("mcps", [])]}
            identifier = self.insert_requirement(db, workspace, title, description, engine, repositories, "sop_queued")
            db.execute("INSERT INTO runs VALUES (?, ?, NULL)", (identifier, json.dumps(snapshot)))
            db.executemany("INSERT INTO steps VALUES (?, ?, 'pending', NULL, NULL)",
                           [(identifier, node["id"]) for node in snapshot["nodes"]])
            self.advance(db, identifier, snapshot)
        return identifier

    def list(self):
        result = super().list()
        with self.db() as db:
            names = {row["requirement"]: json.loads(row["snapshot"])["name"]
                     for row in db.execute("SELECT requirement, snapshot FROM runs")}
        for item in result["requirements"]:
            if item["id"] in names:
                item["sop"] = names[item["id"]]
        return result

    def get(self, identifier):
        item = super().get(identifier)
        with self.db() as db:
            run = db.execute("SELECT * FROM runs WHERE requirement = ?", (identifier,)).fetchone()
            if run:
                snapshot = json.loads(run["snapshot"])
                states = {row["node"]: decode(row, "artifact") for row in db.execute(
                    "SELECT * FROM steps WHERE requirement = ?", (identifier,))}
                item.update(sop=snapshot["name"], executor=run["executor"], nodes=[
                    {"id": node["id"], "name": node["name"], "type": node["type"],
                     "prompt": node["prompt"], "status": states[node["id"]]["status"], "artifact": states[node["id"]]["artifact"]}
                    for node in snapshot["nodes"]])
        return item

    def advance(self, db, identifier, snapshot):
        states = {row["node"]: row["status"] for row in db.execute(
            "SELECT node, status FROM steps WHERE requirement = ?", (identifier,))}
        if all(status == "done" for status in states.values()):
            status = "delivered"
        else:
            node = next(node for node in snapshot["nodes"] if states[node["id"]] == "pending"
                        and all(states[key] == "done" for key in node["depends"]))
            status = "sop_review" if node["type"] == "review" else "sop_queued"
            db.execute("UPDATE steps SET status = ? WHERE requirement = ? AND node = ?",
                       ("review" if node["type"] == "review" else "ready", identifier, node["id"]))
        db.execute("UPDATE requirements SET status = ? WHERE id = ?", (status, identifier))

    def review_node(self, identifier, decision, note):
        if decision not in {"approved", "rejected"} or not isinstance(note, str):
            raise ValueError("无效的审核决定")
        with self.db() as db:
            db.execute("BEGIN IMMEDIATE")
            step = db.execute("SELECT node FROM steps WHERE requirement = ? AND status = 'review'",
                              (identifier,)).fetchone()
            if not step:
                raise ValueError("没有待审核节点")
            db.execute("UPDATE steps SET status = ?, artifact = ? WHERE requirement = ? AND node = ?",
                       ("done" if decision == "approved" else "rejected",
                        json.dumps({"decision": decision, "note": note}), identifier, step["node"]))
            if decision == "approved":
                snapshot = db.execute("SELECT snapshot FROM runs WHERE requirement = ?", (identifier,)).fetchone()[0]
                self.advance(db, identifier, json.loads(snapshot))
            else:
                db.execute("UPDATE requirements SET status = 'sop_rejected' WHERE id = ?", (identifier,))

    def retry(self, identifier):
        if self.get(identifier)["status"] != "sop_failed":
            return super().retry(identifier)
        with self.db() as db:
            db.execute("BEGIN IMMEDIATE")
            changed = db.execute("UPDATE steps SET status = 'ready' WHERE requirement = ? AND status = 'failed'",
                                 (identifier,)).rowcount
            if not changed:
                raise ValueError("没有可以重试的节点")
            db.execute("UPDATE requirements SET status = 'sop_queued' WHERE id = ?", (identifier,))

    def register(self, identifier, name, proof=None):
        if not re.fullmatch(r"[\w-]{1,80}", identifier) or not name.strip():
            raise ValueError("执行器标识或名称无效")
        proof = proof or secrets.token_hex(32)
        with self.db() as db:
            db.execute("BEGIN IMMEDIATE")
            existing = db.execute("SELECT proof FROM executors WHERE id = ?", (identifier,)).fetchone()
            if existing and not secrets.compare_digest(existing["proof"], proof):
                raise ValueError("执行器身份验证失败")
            db.execute("""INSERT INTO executors(id, name, proof, seen) VALUES (?, ?, ?, ?)
                ON CONFLICT(id) DO UPDATE SET name = excluded.name, seen = excluded.seen""",
                       (identifier, name, proof, time.time()))
        return identifier

    def verify_executor(self, identifier, proof):
        with self.db() as db:
            row = db.execute("SELECT proof FROM executors WHERE id = ?", (identifier,)).fetchone()
        return row is not None and isinstance(proof, str) and secrets.compare_digest(row["proof"], proof)

    def executors(self):
        with self.db() as db:
            return [dict(row, online=time.time() - row["seen"] < 45) for row in db.execute("""
                SELECT id, name, seen, enabled,
                    (SELECT COUNT(*) FROM steps JOIN runs USING(requirement)
                     WHERE runs.executor = executors.id AND steps.status = 'running') +
                    (SELECT COUNT(*) FROM drafts WHERE executor = executors.id AND status = 'running') AS running
                FROM executors ORDER BY name""")]

    def set_executor(self, identifier, enabled):
        if type(enabled) is not bool:
            raise ValueError("enabled 必须为布尔值")
        with self.db() as db:
            if not db.execute("UPDATE executors SET enabled = ? WHERE id = ?", (enabled, identifier)).rowcount:
                raise ValueError("执行器不存在")

    def claim_remote(self, identifier):
        lease = uuid4().hex
        with self.db() as db:
            db.execute("BEGIN IMMEDIATE")
            db.execute("UPDATE executors SET seen = ? WHERE id = ?", (time.time(), identifier))
            executor = db.execute("SELECT enabled FROM executors WHERE id = ?", (identifier,)).fetchone()
            if not executor or not executor[0]:
                return None
            row = db.execute("""
                SELECT steps.requirement, steps.node, runs.snapshot
                FROM steps JOIN runs USING(requirement)
                WHERE steps.status = 'ready' AND (runs.executor IS NULL OR runs.executor = ?)
                ORDER BY runs.rowid LIMIT 1""", (identifier,)).fetchone()
            if row:
                db.execute("UPDATE runs SET executor = ? WHERE requirement = ?", (identifier, row["requirement"]))
                db.execute("UPDATE steps SET status = 'running', lease = ? WHERE requirement = ? AND node = ?",
                           (lease, row["requirement"], row["node"]))
                db.execute("UPDATE requirements SET status = 'sop_running' WHERE id = ?", (row["requirement"],))
            else:
                draft = db.execute("""UPDATE drafts SET status = 'running', executor = ?, lease = ?
                    WHERE id = (SELECT id FROM drafts WHERE status = 'queued' ORDER BY rowid LIMIT 1)
                    RETURNING id, workspace, engine, messages""", (identifier, lease)).fetchone()
        if row:
            item = self.get(row["requirement"])
            node = next(node for node in json.loads(row["snapshot"])["nodes"] if node["id"] == row["node"])
            return {"kind": "node", "lease": lease, "item": item, "node": node}
        if draft:
            agents = [{"id": row["id"], "name": row["name"]} for row in self.resources(draft["workspace"])
                      if row["kind"] == "agent"]
            return {"kind": "draft", "lease": lease, **decode(draft, "messages"), "agents": agents}
        return None

    def heartbeat(self, identifier):
        with self.db() as db:
            db.execute("UPDATE executors SET seen = ? WHERE id = ?", (time.time(), identifier))

    def recover_executor(self, identifier):
        with self.db() as db:
            db.execute("BEGIN IMMEDIATE")
            row = db.execute("SELECT enabled, seen FROM executors WHERE id = ?", (identifier,)).fetchone()
            if not row or row["enabled"] or time.time() - row["seen"] < 45:
                raise ValueError("恢复前需停用执行器，并确认其已停止至少 45 秒")
            failure = json.dumps({"error": "执行器失联，节点可以重试"})
            db.execute("""UPDATE requirements SET status = 'sop_failed' WHERE id IN
                (SELECT requirement FROM steps JOIN runs USING(requirement)
                 WHERE runs.executor = ? AND steps.status = 'running')""", (identifier,))
            db.execute("""UPDATE steps SET status = 'failed', artifact = ?
                WHERE status = 'running' AND requirement IN (SELECT requirement FROM runs WHERE executor = ?)""",
                       (failure, identifier))
            db.execute("UPDATE drafts SET status = 'failed', error = ? WHERE status = 'running' AND executor = ?",
                       ("执行器失联，请重新提交对话", identifier))

    def complete_remote(self, identifier, lease, artifact, error=None):
        if not isinstance(artifact, dict) or error is not None and not isinstance(error, str):
            raise ValueError("节点产物格式无效")
        with self.db() as db:
            db.execute("BEGIN IMMEDIATE")
            step = db.execute("""SELECT steps.*, runs.snapshot FROM steps JOIN runs USING(requirement)
                WHERE lease = ? AND executor = ?""", (lease, identifier)).fetchone()
            if step is None:
                draft = db.execute("SELECT * FROM drafts WHERE lease = ? AND executor = ?", (lease, identifier)).fetchone()
                if draft is None:
                    raise ValueError("执行记录不存在")
                if draft["status"] != "running":
                    return
                try:
                    nodes = self.validate_nodes(draft["workspace"], artifact.get("nodes"), db) if not error else None
                except (ValueError, TypeError, AttributeError) as exception:
                    nodes, error = None, str(exception)
                db.execute("UPDATE drafts SET status = ?, nodes = ?, error = ? WHERE id = ?",
                           ("failed" if error else "done", json.dumps(nodes), error, draft["id"]))
                return
            if step["status"] != "running":
                return
            item = super().get(step["requirement"])
            reviews = artifact.get("reviews", {})
            for repo in item["targets"]:
                if repo["id"] in reviews:
                    try:
                        url = validated_review(repo, reviews[repo["id"]])
                    except (RuntimeError, TypeError) as exception:
                        error = str(exception)
                    else:
                        db.execute("UPDATE targets SET review = ? WHERE requirement = ? AND repository = ?",
                                   (url, item["id"], repo["id"]))
            if error:
                artifact = {**artifact, "error": error}
            db.execute("UPDATE steps SET status = ?, artifact = ? WHERE requirement = ? AND node = ?",
                       ("failed" if error else "done", json.dumps(artifact), item["id"], step["node"]))
            if error:
                db.execute("UPDATE requirements SET status = 'sop_failed' WHERE id = ?", (item["id"],))
            else:
                self.advance(db, item["id"], json.loads(step["snapshot"]))

    def draft(self, workspace, engine, messages):
        if (engine not in ENGINES or not isinstance(messages, list) or not 0 < len(messages) <= 20
                or not all(isinstance(turn, dict) and turn.get("role") in {"user", "assistant"}
                           and isinstance(turn.get("content"), str) for turn in messages)
                or messages[-1]["role"] != "user"):
            raise ValueError("AI 对话格式无效")
        identifier = uuid4().hex
        with self.db() as db:
            db.execute("""INSERT INTO drafts(id, workspace, engine, messages, status)
                        VALUES (?, ?, ?, ?, 'queued')""", (identifier, workspace, engine, json.dumps(messages)))
        return identifier

    def get_draft(self, identifier):
        with self.db() as db:
            row = db.execute("SELECT id, status, nodes, error FROM drafts WHERE id = ?", (identifier,)).fetchone()
        if not row:
            raise ValueError("对话草稿不存在")
        return decode(row, "nodes")
