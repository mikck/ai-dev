import tempfile
import unittest
from pathlib import Path

from workflow import WorkflowPlatform


class WorkflowTest(unittest.TestCase):
    def test_graph_review_and_executor_affinity(self):
        with tempfile.TemporaryDirectory() as root:
            platform = WorkflowPlatform(Path(root))
            workspace = platform.workspace("demo")
            agent = platform.resource(workspace, "agent", "Builder", {"engine": "codex"})
            repo = platform.repository(workspace, "github", "https://github.com/org/demo", "main")
            nodes = [
                {"id": "build", "name": "Build", "type": "agent", "agent": agent},
                {"id": "review", "name": "Review", "type": "review", "depends": ["build"]},
                {"id": "finish", "name": "Finish", "type": "agent", "agent": agent, "depends": ["review"]},
            ]
            with self.assertRaises(ValueError):
                platform.save_sop(workspace, "Cycle", [{**nodes[0], "depends": ["review"]}, nodes[1]])
            sop = platform.save_sop(workspace, "Delivery", nodes)
            task = platform.requirement(workspace, "Title", "Details", "codex", [repo], sop=sop)
            one = platform.register("runner-one", "Executor 1")
            two = platform.register("runner-two", "Executor 2")
            first = platform.claim_remote(one)
            self.assertEqual(first["node"]["id"], "build")
            platform.complete_remote(one, first["lease"], {"output": "first"})
            self.assertIsNone(platform.claim_remote(two))
            platform.review_node(task, "approved", "Looks good")
            final = platform.claim_remote(one)
            self.assertEqual(final["item"]["id"], task)
            self.assertEqual(final["node"]["id"], "finish")
            platform.complete_remote(one, final["lease"], {"output": "last"})
            self.assertEqual(platform.get(task)["status"], "delivered")
            self.assertEqual([n["artifact"] for n in platform.get(task)["nodes"]],
                             [{"output": "first"}, {"decision": "approved", "note": "Looks good"}, {"output": "last"}])

    def test_workspace_resources_and_sop_snapshot(self):
        with tempfile.TemporaryDirectory() as root:
            platform = WorkflowPlatform(Path(root))
            first = platform.workspace("first")
            other = platform.workspace("other")
            skill = platform.resource(first, "skill", "Guidance", {"content": "Use tests"})
            with self.assertRaises(ValueError):
                platform.resource(other, "agent", "Invalid", {"engine": "codex", "skills": [skill]})
            agent = platform.resource(first, "agent", "Builder", {"engine": "claude", "skills": [skill]})
            node = {"id": "one", "name": "Execute", "type": "agent", "agent": agent}
            with self.assertRaises(ValueError):
                platform.save_sop(other, "Bad references", [node])
            sop = platform.save_sop(first, "Original", [node])
            repo = platform.repository(first, "github", "https://github.com/org/demo", "main")
            task = platform.requirement(first, "Title", "Details", "codex", [repo], sop=sop)
            with self.assertRaises(ValueError):
                platform.delete_resource(first, skill)
            platform.save_sop(first, "Updated", [{"id": "review", "name": "Review", "type": "review"}], sop)
            platform.delete_resource(first, agent)
            claim = platform.claim_remote(platform.register("worker", "Worker"))
            self.assertEqual(claim["item"]["id"], task)
            self.assertEqual(claim["item"]["sop"], "Original")
            self.assertEqual(claim["node"]["runtime"]["engine"], "claude")
            self.assertEqual(claim["node"]["runtime"]["skills"][0]["config"]["content"], "Use tests")

    def test_http_executor_creates_and_reuses_git_worktrees(self):
        import json
        import os
        import sys
        import threading
        from http.server import ThreadingHTTPServer
        from urllib.error import HTTPError
        from unittest.mock import patch

        from delivery import run
        from executor import Remote, process
        from server import handler_for

        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            platform = WorkflowPlatform(root / "management")
            workspace = platform.workspace("Delivery")
            agent = platform.resource(workspace, "agent", "Builder", {"engine": "codex"})
            bin_dir = root / "bin"
            bin_dir.mkdir()
            env = {
                "PATH": f"{bin_dir}:{os.environ['PATH']}",
                "GIT_AUTHOR_NAME": "Test", "GIT_AUTHOR_EMAIL": "test@example.com",
                "GIT_COMMITTER_NAME": "Test", "GIT_COMMITTER_EMAIL": "test@example.com",
            }
            repos = []
            remote_dirs = []
            with patch.dict(os.environ, env):
                for index, (provider, url) in enumerate((
                    ("github", "https://github.com/org/api.git"), ("gitlab", "https://gitlab.com/org/web.git")
                )):
                    path = root / f"repository-{index}"
                    run(["git", "init", "-b", "main", str(path)])
                    (path / "README").write_text("initial")
                    run(["git", "add", "README"], path)
                    run(["git", "commit", "-m", "Initial"], path)
                    remote_dirs.append(path)
                    repos.append(platform.repository(workspace, provider, url, "main", "test -f artifact.txt"))
                    env[f"GIT_CONFIG_KEY_{index}"] = f"url.{path}.insteadOf"
                    env[f"GIT_CONFIG_VALUE_{index}"] = url
                env["GIT_CONFIG_COUNT"] = "2"
            for program in ("codex", "claude"):
                script = bin_dir / program
                script.write_text(f"""#!{sys.executable}
import json, pathlib, subprocess, sys
prompt = sys.stdin.read()
data = json.loads(prompt.split("\\n", 1)[1])
if "agents" in data:
    print(json.dumps({{"nodes": [{{"id": "build", "name": "Build", "type": "agent",
                                     "agent": data["agents"][0]["id"]}}]}}))
    sys.exit()
for repo in data["repositories"]:
    if "create_worktree" in repo:
        subprocess.run(repo["create_worktree"], shell=True, check=True)
    else:
        assert data["previous_artifacts"][0]["artifact"]["commits"]
    path = pathlib.Path(repo["worktree"])
    with (path / "artifact.txt").open("a") as stream:
        stream.write(data["node"] + "\\n")
    subprocess.run(["git", "add", "artifact.txt"], cwd=path, check=True)
    subprocess.run(["git", "commit", "-m", data["node"]], cwd=path, check=True)
    if "publish" in repo:
        subprocess.run(repo["publish"], shell=True, check=True)
print("Agent completed")
""")
                script.chmod(0o755)
            for program, link in (("gh", "https://github.com/org/api/pull/1"),
                                  ("glab", "https://gitlab.com/org/web/-/merge_requests/1")):
                script = bin_dir / program
                script.write_text(f"#!{sys.executable}\nimport sys\nprint('[]' if sys.argv[2] == 'list' else {link!r})\n")
                script.chmod(0o755)
            sop = platform.save_sop(workspace, "Delivery SOP", [
                {"id": "build", "name": "Build", "type": "agent", "agent": agent},
                {"id": "review", "name": "Review", "type": "review", "depends": ["build"]},
                {"id": "push", "name": "Publish", "type": "agent", "agent": agent, "depends": ["review"], "publish": True},
            ])
            task = platform.requirement(workspace, "Title", "Details", "codex", repos, sop=sop)
            server = ThreadingHTTPServer(("127.0.0.1", 0), handler_for(platform, "test-token"))
            thread = threading.Thread(target=server.serve_forever, daemon=True)
            thread.start()
            try:
                url = f"http://127.0.0.1:{server.server_port}"
                remote = Remote(url, "runner", "a" * 64, "test-token")
                with self.assertRaises(HTTPError) as auth:
                    Remote(url, "runner", "a" * 64, "incorrect").call("register", name="Runner")
                self.assertEqual(auth.exception.code, 401)
                remote.call("register", name="Runner")
                self.assertTrue(platform.executors()[0]["online"])
                platform.set_executor("runner", False)
                self.assertIsNone(remote.call("claim")["job"])
                platform.set_executor("runner", True)

                def finish_job():
                    claimed = remote.call("claim")["job"]
                    result = process(root / "executor", claimed)
                    self.assertIsNone(result["error"], result)
                    remote.call("complete", lease=claimed["lease"], **result)

                with patch.dict(os.environ, env):
                    finish_job()
                    self.assertEqual(platform.get(task)["status"], "sop_review")
                    platform.review_node(task, "approved", "Proceed")
                    finish_job()
                    draft = platform.draft(workspace, "claude", [{"role": "user", "content": "Make a workflow"}])
                    finish_job()
                    self.assertEqual(platform.get_draft(draft)["nodes"][0]["agent"], agent)

                result = platform.get(task)
                self.assertEqual(result["status"], "delivered")
                self.assertTrue(all(repo["review"] for repo in result["targets"]))
                for index, identifier in enumerate(repos):
                    checkout = root / "executor" / "jobs" / task / "worktrees" / identifier
                    self.assertTrue((checkout / ".git").is_file())
                    self.assertEqual(run(["git", "branch", "--show-current"], checkout), "delivery/" + task)
                    self.assertEqual((checkout / "artifact.txt").read_text(), "Build\nPublish\n")
                    self.assertEqual(run(["git", "rev-parse", "delivery/" + task], remote_dirs[index]),
                                     result["nodes"][2]["artifact"]["commits"][identifier])
                    self.assertNotEqual(result["nodes"][0]["artifact"]["commits"][identifier],
                                        result["nodes"][2]["artifact"]["commits"][identifier])
                self.assertFalse((root / "executor" / "delivery.db").exists())
            finally:
                server.shutdown()
                server.server_close()
                thread.join()
