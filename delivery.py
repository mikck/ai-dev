"""SQLite-backed delivery queue for a single trusted deployment."""
import json
import re
import sqlite3
import subprocess
import sys
from contextlib import contextmanager
from pathlib import Path
from urllib.parse import urlsplit
from uuid import uuid4

HOSTS = {"github": "github.com", "gitlab": "gitlab.com"}
ENGINES = {"codex", "claude"}
SCHEMA = """
CREATE TABLE IF NOT EXISTS workspaces (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE);
CREATE TABLE IF NOT EXISTS repositories (
    id TEXT PRIMARY KEY, workspace TEXT NOT NULL REFERENCES workspaces,
    provider TEXT NOT NULL, url TEXT NOT NULL, base TEXT NOT NULL, verify TEXT NOT NULL,
    UNIQUE(workspace, url)
);
CREATE TABLE IF NOT EXISTS requirements (
    id TEXT PRIMARY KEY, workspace TEXT NOT NULL REFERENCES workspaces,
    title TEXT NOT NULL, description TEXT NOT NULL, engine TEXT NOT NULL,
    status TEXT NOT NULL, plan TEXT, log TEXT
);
CREATE TABLE IF NOT EXISTS targets (
    requirement TEXT NOT NULL REFERENCES requirements,
    repository TEXT NOT NULL REFERENCES repositories,
    review TEXT, PRIMARY KEY(requirement, repository)
);
"""


def run(args, cwd=None, prompt=None, timeout=1800):
    result = subprocess.run(args, cwd=cwd, input=prompt, text=True,
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                            timeout=timeout, check=False)
    if result.returncode:
        raise RuntimeError(f"{args[0]} exited {result.returncode}\n{result.stderr[-8000:]}")
    return result.stdout.strip()


class Platform:
    def __init__(self, root, *, initialize=True):
        self.root = root.resolve()
        if initialize:
            self.root.mkdir(parents=True, exist_ok=True)
            with self.db() as db:
                db.executescript(SCHEMA)

    @contextmanager
    def db(self):
        db = sqlite3.connect(self.root / "delivery.db")
        db.row_factory = sqlite3.Row
        db.execute("PRAGMA foreign_keys = ON")
        try:
            with db:
                yield db
        finally:
            db.close()

    def workspace(self, name):
        if not name.strip():
            raise ValueError("请输入 workspace 名称")
        identifier = uuid4().hex
        with self.db() as db:
            db.execute("INSERT INTO workspaces VALUES (?, ?)", (identifier, name.strip()))
        return identifier

    def repository(self, workspace, provider, url, base, verify=""):
        parsed = urlsplit(url)
        if (provider not in HOSTS or parsed.scheme != "https"
                or parsed.netloc != HOSTS[provider] or parsed.query or parsed.fragment
                or not re.fullmatch(r"(?:/[A-Za-z0-9_.-]+){2,}", parsed.path)
                or any(part in {".", ".."} for part in parsed.path.split("/"))):
            raise ValueError("请输入受支持平台上无凭据的 HTTPS 仓库地址")
        if not base or base.startswith("-"):
            raise ValueError("请输入有效的目标分支")
        try:
            run(["git", "check-ref-format", f"refs/heads/{base}"])
        except RuntimeError as error:
            raise ValueError("请输入有效的目标分支") from error
        identifier = uuid4().hex
        with self.db() as db:
            db.execute("INSERT INTO repositories VALUES (?, ?, ?, ?, ?, ?)",
                       (identifier, workspace, provider, url.removesuffix(".git") + ".git", base, verify))
        return identifier

    def requirement(self, workspace, title, description, engine, repositories):
        with self.db() as db:
            return self.insert_requirement(db, workspace, title, description, engine, repositories, "queued_plan")

    def insert_requirement(self, db, workspace, title, description, engine, repositories, status):
        if engine not in ENGINES or not title.strip() or not description.strip() or not repositories:
            raise ValueError("请输入需求标题、说明，并选择执行工具和仓库")
        identifier = uuid4().hex
        allowed = {row["id"] for row in db.execute(
            "SELECT id FROM repositories WHERE workspace = ?", (workspace,))}
        if not set(repositories) <= allowed:
            raise ValueError("需求只能关联所属 workspace 的仓库")
        db.execute("INSERT INTO requirements VALUES (?, ?, ?, ?, ?, ?, NULL, NULL)",
                   (identifier, workspace, title, description, engine, status))
        db.executemany("INSERT INTO targets VALUES (?, ?, NULL)",
                       [(identifier, repo) for repo in dict.fromkeys(repositories)])
        return identifier

    def list(self):
        with self.db() as db:
            return {table: [dict(row) for row in db.execute(f"SELECT * FROM {table} ORDER BY rowid DESC")]
                    for table in ("workspaces", "repositories", "requirements")}

    def get(self, identifier):
        with self.db() as db:
            row = db.execute("SELECT * FROM requirements WHERE id = ?", (identifier,)).fetchone()
            if row is None:
                raise ValueError("需求不存在")
            item = dict(row)
            item["targets"] = [dict(target) for target in db.execute(
                """SELECT repositories.*, targets.review FROM targets
                   JOIN repositories ON repositories.id = targets.repository
                   WHERE targets.requirement = ? ORDER BY targets.rowid""", (identifier,))]
            return item

    def transition(self, identifier, current, next_status):
        with self.db() as db:
            if not db.execute("UPDATE requirements SET status = ? WHERE id = ? AND status = ?",
                              (next_status, identifier, current)).rowcount:
                raise ValueError("当前状态不允许该操作")

    def approve(self, identifier):
        self.transition(identifier, "awaiting_approval", "queued_execute")

    def retry(self, identifier):
        status = self.get(identifier)["status"]
        queue = {"failed_plan": "queued_plan", "failed_execute": "queued_execute"}
        if status not in queue:
            raise ValueError("只有失败的任务可以重试")
        self.transition(identifier, status, queue[status])

    def claim(self):
        with self.db() as db:
            row = db.execute("""UPDATE requirements
                SET status = CASE status WHEN 'queued_plan' THEN 'planning' ELSE 'executing' END
                WHERE id = (SELECT id FROM requirements WHERE status IN ('queued_plan', 'queued_execute')
                            ORDER BY rowid LIMIT 1)
                RETURNING id""").fetchone()
        return self.get(row["id"]) if row else None

    def checkout(self, item, repo):
        return self.root / "jobs" / item["id"] / repo["id"]

    def branch(self, item):
        return f"delivery/{item['id']}"

    def execute_next(self):
        item = self.claim()
        if item is None:
            return False
        planning = item["status"] == "planning"
        try:
            try:
                output = self.invoke(item, planning)
            finally:
                if not planning:
                    self.collect_reviews(item)
            status = "awaiting_approval" if planning else "delivered"
            if planning and not output:
                raise RuntimeError("AI 未返回计划")
            if not planning and not any(repo["review"] for repo in self.get(item["id"])["targets"]):
                raise RuntimeError("AI 未发布任何 PR 或 MR\n" + output)
            if not planning:
                for repo in self.get(item["id"])["targets"]:
                    path = self.checkout(item, repo)
                    dirty = run(["git", "status", "--porcelain"], path)
                    changed = run(["git", "diff", "--name-only", f"origin/{repo['base']}...HEAD"], path)
                    if dirty or (changed and not repo["review"]):
                        raise RuntimeError(f"仓库 {repo['url']} 仍有未交付的变更")
                    if repo["review"]:
                        local = run(["git", "rev-parse", "HEAD"], path)
                        pushed = run(["git", "rev-parse", f"origin/{self.branch(item)}"], path)
                        if local != pushed:
                            raise RuntimeError(f"仓库 {repo['url']} 有发布后的新提交")
        except (OSError, RuntimeError, subprocess.TimeoutExpired) as error:
            status = "failed_plan" if planning else "failed_execute"
            output = str(error)
        with self.db() as db:
            if planning and status == "awaiting_approval":
                db.execute("UPDATE requirements SET status = ?, plan = ?, log = NULL WHERE id = ?",
                           (status, output, item["id"]))
            else:
                db.execute("UPDATE requirements SET status = ?, log = ? WHERE id = ?",
                           (status, output, item["id"]))
        return True

    def invoke(self, item, planning):
        from shlex import join

        job = self.root / "jobs" / item["id"]
        job.mkdir(parents=True, exist_ok=True)
        repositories = []
        for repo in item["targets"]:
            path = self.checkout(item, repo)
            if not path.exists():
                temporary = path.with_name(f"{path.name}-{uuid4().hex}.clone")
                run(["git", "clone", "--branch", repo["base"], "--", repo["url"], str(temporary)])
                run(["git", "switch", "-c", self.branch(item)], temporary)
                temporary.rename(path)
            helper = join([sys.executable, str(Path(__file__).resolve()), "--root", str(self.root),
                           "publish", item["id"], repo["id"]])
            repositories.append({"directory": str(path), "url": repo["url"], "target": repo["base"],
                                 "verification": repo["verify"], "existing_review": repo["review"],
                                 **({"publish": helper} if not planning else {})})
        context = json.dumps(repositories, ensure_ascii=False)
        prompt = f"需求标题：{item['title']}\n需求说明：{item['description']}\n仓库：{context}\n"
        if planning:
            prompt += "只读分析。输出各仓库改动、依赖和验证计划，禁止改动文件及发布。"
        else:
            prompt += (f"已批准的计划：\n{item['plan']}\n"
                       "按计划修改代码，运行验证并提交到已检出的任务分支。"
                       "对每个有改动的仓库执行其 publish 命令，推送分支并创建草稿 PR 或 MR。"
                       "保留已经发布的提交。报告所有检查和发布结果。")
        if item["engine"] == "codex":
            command = ["codex", "exec", "--skip-git-repo-check", "--sandbox",
                       "read-only" if planning else "workspace-write"]
            if not planning:
                command.extend(["-c", "sandbox_workspace_write.network_access=true"])
            command.append("-")
        else:
            command = ["claude", "-p", "--permission-mode", "plan" if planning else "acceptEdits"]
            if not planning:
                command.extend(["--allowedTools", "Bash"])
        return run(command, job, prompt)

    def receipt(self, item, repo):
        return self.checkout(item, repo).with_suffix(".review")

    def collect_reviews(self, item):
        with self.db() as db:
            for repo in item["targets"]:
                receipt = self.receipt(item, repo)
                if receipt.is_file():
                    review = validated_review(repo, receipt.read_text())
                    db.execute("UPDATE targets SET review = ? WHERE requirement = ? AND repository = ?",
                               (review, item["id"], repo["id"]))

    def publish(self, identifier, repository):
        item = self.get(identifier)
        repo = next((repo for repo in item["targets"] if repo["id"] == repository), None)
        if item["status"] != "executing" or repo is None:
            raise ValueError("只能发布正在执行的需求所关联的仓库")
        path = self.checkout(item, repo)
        branch = self.branch(item)
        if run(["git", "branch", "--show-current"], path) != branch:
            raise ValueError("当前分支不是需求分支")
        if run(["git", "status", "--porcelain"], path):
            raise ValueError("发布前请提交工作目录中的变更")
        if not run(["git", "diff", "--name-only", f"origin/{repo['base']}...HEAD"], path):
            raise ValueError("需求分支没有可发布的变更")
        if repo["verify"]:
            run(["sh", "-ec", repo["verify"]], path)
        run(["git", "push", repo["url"], f"HEAD:refs/heads/{branch}"], path)
        run(["git", "fetch", repo["url"], f"refs/heads/{branch}:refs/remotes/origin/{branch}"], path)
        receipt = self.receipt(item, repo)
        previous = receipt.read_text() if receipt.is_file() else repo["review"]
        review = validated_review(repo, previous or create_review(item, repo, branch, path))
        temporary = receipt.with_suffix(".review.tmp")
        temporary.write_text(review)
        temporary.replace(receipt)
        return review


def validated_review(repo, review):
    project = urlsplit(repo["url"]).path.removesuffix(".git")
    suffix = "/pull/" if repo["provider"] == "github" else "/-/merge_requests/"
    prefix = f"https://{HOSTS[repo['provider']]}{project}{suffix}"
    if not re.fullmatch(re.escape(prefix) + r"[0-9]+", review, flags=re.IGNORECASE):
        raise RuntimeError("发布工具未返回有效的审核链接")
    return review


def create_review(item, repo, branch, path):
    title = item["title"]
    body = f"Delivery: {item['id']}"
    base = repo["base"]
    project = urlsplit(repo["url"]).path[1:].removesuffix(".git")
    if repo["provider"] == "github":
        existing = json.loads(run(["gh", "pr", "list", "--repo", project, "--head", branch,
                                   "--base", base, "--state", "open", "--json", "url"], path))
        if existing:
            return existing[0]["url"]
        return run(["gh", "pr", "create", "--repo", project, "--head", branch,
                    "--base", base, "--title", title, "--body", body, "--draft"], path)
    existing = json.loads(run(["glab", "mr", "list", "--repo", project, "--source-branch", branch,
                               "--target-branch", base, "--output", "json"], path))
    if existing:
        return existing[0]["web_url"]
    result = run(["glab", "mr", "create", "--repo", project, "--source-branch", branch,
                  "--target-branch", base, "--title", title, "--description", body,
                  "--draft", "--yes"], path)
    urls = re.findall(r"https://gitlab\.com/[^\s]+/-/merge_requests/\d+", result)
    if not urls:
        raise RuntimeError("GitLab 未返回 MR 链接")
    return urls[-1]


if __name__ == "__main__":
    import argparse
    import time

    parser = argparse.ArgumentParser()
    parser.add_argument("--root", type=Path, default=Path(".delivery"))
    commands = parser.add_subparsers(dest="command", required=True)
    commands.add_parser("worker")
    publish = commands.add_parser("publish")
    publish.add_argument("requirement")
    publish.add_argument("repository")
    args = parser.parse_args()
    platform = Platform(args.root, initialize=args.command != "publish")
    if args.command == "publish":
        print(platform.publish(args.requirement, args.repository))
    else:
        while True:
            if not platform.execute_next():
                time.sleep(1)
