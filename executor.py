"""Register with the management plane and run assigned jobs locally."""
import argparse
import fcntl
import json
import os
import re
import secrets
import shlex
import shutil
import sys
import threading
import time
from pathlib import Path
from urllib.error import HTTPError
from urllib.parse import urlsplit
from urllib.request import Request, urlopen
from uuid import uuid4

from delivery import Platform, run

SCRIPT = str(Path(__file__).resolve())


def save_json(path, value):
    temporary = path.with_name(path.name + ".tmp")
    with temporary.open("w", encoding="utf-8") as file:
        json.dump(value, file, ensure_ascii=False)
    temporary.replace(path)


class JobPlatform(Platform):
    def __init__(self, job, item):
        self.job = job
        self.item = item

    def get(self, identifier):
        if identifier != self.item["id"]:
            raise ValueError("需求和执行目录不匹配")
        return self.item

    def checkout(self, item, repo):
        return self.job / "worktrees" / repo["id"]

    def prepare_metadata(self, repo):
        directory = self.checkout(self.item, repo)
        directory.parent.mkdir(exist_ok=True)
        metadata = self.job / "git" / repo["id"]
        metadata.parent.mkdir(exist_ok=True)
        if not metadata.exists():
            temporary = metadata.with_name(metadata.name + "-" + uuid4().hex)
            try:
                run(["git", "clone", "--bare", "--branch", repo["base"], "--", repo["url"], str(temporary)])
                run(["git", "update-ref", f"refs/remotes/origin/{repo['base']}",
                     f"refs/heads/{repo['base']}"], temporary)
                temporary.rename(metadata)
            finally:
                if temporary.exists():
                    shutil.rmtree(temporary)
        return metadata


def cli_command(engine, planning, job, mcps=()):
    if engine == "codex":
        command = ["codex", "-a", "never", "exec", "--skip-git-repo-check", "--sandbox",
                   "read-only" if planning else "workspace-write"]
        if not planning:
            command.extend(["-c", "sandbox_workspace_write.network_access=true"])
        for server in mcps:
            name, config = server["id"], server["config"]
            command.extend(["-c", f"mcp_servers.s_{name}.command={json.dumps(config['command'])}",
                            "-c", f"mcp_servers.s_{name}.args={json.dumps(config.get('args', []))}"])
        return [*command, "-"]
    command = ["claude", "-p", "--permission-mode", "plan" if planning else "acceptEdits"]
    if not planning:
        command.extend(["--allowedTools", "Bash"])
    configuration = job / "mcp.json"
    save_json(configuration, {"mcpServers": {
        "s_" + server["id"]: {"command": server["config"]["command"],
                              "args": server["config"].get("args", [])}
        for server in mcps}})
    return [*command, "--strict-mcp-config", "--mcp-config", str(configuration)]


def generate(job, draft):
    prompt = ("你是 SOP 编排助手。结合对话生成草稿。仅输出 JSON 对象，含 nodes 数组。"
              "节点含 id、name、type（agent 或 review）、depends（前置节点 ID 数组）、prompt。"
              "agent 节点还含 agent（下方真实 Agent ID）和 publish（是否创建 PR/MR）。"
              "保持 DAG 简洁。\n" + json.dumps({"agents": draft["agents"], "messages": draft["messages"]},
                                              ensure_ascii=False))
    output = run(cli_command(draft["engine"], True, job), job, prompt)
    output = re.sub(r"^\s*```(?:json)?\s*|\s*```\s*$", "", output.strip())
    return json.loads(output)


def execute(job, assignment, artifact):
    item, node = assignment["item"], assignment["node"]
    runtime = node["runtime"]
    job_platform = JobPlatform(job, {**item, "status": "executing"})
    manifest = job / "execution.json"
    repositories = []
    try:
        for repo in item["targets"]:
            metadata = job_platform.prepare_metadata(repo)
            directory = job_platform.checkout(item, repo)
            row = {"url": repo["url"], "worktree": str(directory), "target": repo["base"],
                   "branch": job_platform.branch(item), "verify": repo["verify"], "review": repo["review"]}
            if not directory.exists():
                row["create_worktree"] = shlex.join([
                    "git", "--git-dir", str(metadata), "worktree", "add", "-b",
                    job_platform.branch(item), str(directory), f"refs/heads/{repo['base']}"])
            if node["publish"]:
                row["publish"] = shlex.join([sys.executable, SCRIPT, "publish", str(manifest), repo["id"]])
            repositories.append(row)
        save_json(manifest, {"item": job_platform.item, "publish": node["publish"]})
        prompt = (
            "执行当前 SOP 节点。对提供 create_worktree 的仓库先运行该命令，"
            "由你创建 Git worktree。已存在的 worktree 直接复用。"
            "仅在列出的 Git worktree 修改，保留当前任务分支。"
            "完成后提交节点变更、运行验证并输出产物摘要。"
            "仅当仓库提供 publish 命令时可发布，需对有变更的仓库调用对应 publish 命令。\n"
            + json.dumps({"requirement": item["title"], "description": item["description"],
                          "node": node["name"], "instructions": node["prompt"], "agent": runtime["prompt"],
                          "skills": [skill["config"]["content"] for skill in runtime["skills"]],
                          "previous_artifacts": [step for step in item["nodes"] if step["status"] == "done"],
                          "repositories": repositories}, ensure_ascii=False))
        artifact["output"] = run(cli_command(runtime["engine"], False, job, runtime["mcps"]), job, prompt)[-12000:]
        for repo in item["targets"]:
            directory = job_platform.checkout(item, repo)
            metadata = job / "git" / repo["id"]
            if not directory.is_dir() or not (directory / ".git").is_file() or Path(
                    run(["git", "rev-parse", "--path-format=absolute", "--git-common-dir"], directory)
                ).resolve() != metadata.resolve():
                raise RuntimeError(f"Agent 未创建所需的 Git worktree：{repo['url']}")
            if run(["git", "branch", "--show-current"], directory) != job_platform.branch(item):
                raise RuntimeError(f"节点更改了任务分支：{repo['url']}")
            if run(["git", "status", "--porcelain"], directory):
                raise RuntimeError(f"仓库仍有未提交的变更：{repo['url']}")
            if node["publish"] and run(["git", "diff", "--name-only", f"origin/{repo['base']}...HEAD"], directory):
                if not job_platform.receipt(item, repo).exists():
                    raise RuntimeError(f"未创建审核请求：{repo['url']}")
                head = run(["git", "rev-parse", "HEAD"], directory)
                remote = run(["git", "rev-parse", f"origin/{job_platform.branch(item)}"], directory)
                if head != remote:
                    raise RuntimeError(f"仓库有发布后的新提交：{repo['url']}")
    finally:
        if manifest.exists():
            manifest.unlink()
        for repo in item["targets"]:
            directory = job_platform.checkout(item, repo)
            if directory.exists():
                artifact["commits"][repo["id"]] = run(["git", "rev-parse", "HEAD"], directory)
            receipt = job_platform.receipt(item, repo)
            if receipt.exists():
                artifact["reviews"][repo["id"]] = receipt.read_text()


class Remote:
    def __init__(self, url, identifier, proof, token):
        parsed = urlsplit(url)
        if (parsed.scheme != "https"
                and not (parsed.scheme == "http" and parsed.hostname in {"127.0.0.1", "::1", "localhost"})):
            raise ValueError("远程管理面需要 HTTPS")
        self.url = url.rstrip("/")
        self.identity = {"identifier": identifier, "proof": proof}
        self.token = token

    def call(self, action, **payload):
        request = Request(f"{self.url}/api/worker/{action}",
                          data=json.dumps({**self.identity, **payload}).encode(),
                          headers={"Authorization": "Bearer " + self.token,
                                   "Content-Type": "application/json"})
        with urlopen(request, timeout=15) as response:
            return json.load(response)


def keep_alive(remote, stopped):
    while not stopped.wait(10):
        try:
            remote.call("heartbeat")
        except OSError as error:
            print(f"Heartbeat failed: {error}", file=sys.stderr)


def process(root, assignment):
    artifact = {"commits": {}, "reviews": {}}
    try:
        if assignment["kind"] == "draft":
            with_temp = root / "drafts" / assignment["id"]
            with_temp.mkdir(parents=True, exist_ok=True)
            return {"artifact": generate(with_temp, assignment), "error": None}
        job = root / "jobs" / assignment["item"]["id"]
        job.mkdir(parents=True, exist_ok=True)
        execute(job, assignment, artifact)
        return {"artifact": artifact, "error": None}
    except Exception as error:
        return {"artifact": artifact, "error": str(error)[-10000:] or type(error).__name__}


def serve(root, url, identifier, name, token):
    root = root.resolve()
    root.mkdir(parents=True, exist_ok=True)
    with (root / "executor.lock").open("w") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        identity = root / "executor.json"
        if identity.exists():
            stored = json.loads(identity.read_text())
            if stored["id"] != identifier:
                raise ValueError("该执行目录已绑定其他执行器 ID")
        else:
            stored = {"id": identifier, "proof": secrets.token_hex(32)}
            with os.fdopen(os.open(identity, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600), "w") as file:
                json.dump(stored, file)
        remote = Remote(url, identifier, stored["proof"], token)
        remote.call("register", name=name)
        pending = root / "pending-report.json"
        stopped = threading.Event()
        thread = threading.Thread(target=keep_alive, args=(remote, stopped), daemon=True)
        thread.start()
        try:
            while True:
                try:
                    if pending.exists():
                        remote.call("complete", **json.loads(pending.read_text()))
                        pending.unlink()
                    assignment = remote.call("claim")["job"]
                    if not assignment:
                        time.sleep(2)
                        continue
                    save_json(pending, {"lease": assignment["lease"], **process(root, assignment)})
                except HTTPError as error:
                    if error.code < 500:
                        raise
                    time.sleep(5)
                except OSError as error:
                    print(f"Retrying management plane: {error}", file=sys.stderr)
                    time.sleep(5)
        finally:
            stopped.set()
            thread.join()


def main():
    parser = argparse.ArgumentParser()
    commands = parser.add_subparsers(dest="command", required=True)
    daemon = commands.add_parser("start")
    daemon.add_argument("--root", type=Path, default=Path(".executor"))
    daemon.add_argument("--url", default="http://127.0.0.1:8000")
    daemon.add_argument("--id", default="local")
    daemon.add_argument("--name", default="Local executor")
    publish = commands.add_parser("publish")
    publish.add_argument("manifest", type=Path)
    publish.add_argument("repository")
    args = parser.parse_args()
    if args.command == "publish":
        execution = json.loads(args.manifest.read_text())
        if not execution["publish"]:
            raise ValueError("当前节点未配置发布")
        item = execution["item"]
        print(JobPlatform(args.manifest.parent, item).publish(item["id"], args.repository))
    else:
        token = os.environ.pop("DELIVERY_EXECUTOR_TOKEN", "")
        if not token:
            parser.error("请设置 DELIVERY_EXECUTOR_TOKEN")
        serve(args.root, args.url, args.id, args.name, token)


if __name__ == "__main__":
    main()
