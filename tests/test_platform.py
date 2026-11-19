import tempfile
import unittest
from pathlib import Path

from delivery import Platform


class PlatformTest(unittest.TestCase):
    def test_workspace_membership_and_approval(self):
        with tempfile.TemporaryDirectory() as temporary:
            platform = Platform(Path(temporary))
            a = platform.workspace("System A")
            b = platform.workspace("System B")
            github = platform.repository(a, "github", "https://github.com/org/api.git", "main")
            gitlab = platform.repository(a, "gitlab", "https://gitlab.com/org/web.git", "main")
            foreign = platform.repository(b, "github", "https://github.com/other/api.git", "main")
            with self.assertRaises(ValueError):
                platform.requirement(a, "Wrong workspace", "Implement", "codex", [foreign])
            one = platform.requirement(a, "First", "Implement", "codex", [github, gitlab])
            two = platform.requirement(a, "Second", "Implement", "claude", [github])
            with self.assertRaises(ValueError):
                platform.approve(one)
            self.assertNotEqual(one, two)
            self.assertEqual(platform.get(one)["status"], "queued_plan")
            self.assertEqual(platform.get(two)["workspace"], a)

    def test_dual_provider_execution_and_retry(self):
        import json
        import os
        import sys
        from unittest.mock import patch

        from delivery import run

        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            platform = Platform(root / "state")
            workspace = platform.workspace("Integration")
            environment = {"GIT_AUTHOR_NAME": "Test", "GIT_AUTHOR_EMAIL": "test@example.com",
                           "GIT_COMMITTER_NAME": "Test", "GIT_COMMITTER_EMAIL": "test@example.com"}
            bin_dir = root / "bin"
            bin_dir.mkdir()
            environment["PATH"] = f"{bin_dir}:{os.environ['PATH']}"
            urls = ["https://github.com/org/api.git", "https://gitlab.com/org/web.git"]
            target_ids = []
            with patch.dict(os.environ, environment):
                for index, (provider, url) in enumerate(zip(("github", "gitlab"), urls)):
                    remote = root / f"repo-{index}"
                    run(["git", "init", "-b", "main", str(remote)])
                    (remote / "README.md").write_text("Example")
                    run(["git", "add", "."], remote)
                    run(["git", "commit", "-m", "Initial"], remote)
                    target_ids.append(platform.repository(workspace, provider, url, "main",
                                                          "test -f delivered.txt"))
                    environment[f"GIT_CONFIG_KEY_{index}"] = f"url.{remote}.insteadOf"
                    environment[f"GIT_CONFIG_VALUE_{index}"] = url
                environment["GIT_CONFIG_COUNT"] = "2"

                agent_script = f"""#!{sys.executable}
import json, pathlib, subprocess, sys
prompt = sys.stdin.read()
repositories = json.loads(prompt.split("仓库：", 1)[1].splitlines()[0])
if "publish" not in repositories[0]:
    print("Modify both repositories and verify delivered.txt")
else:
    for repo in repositories:
        if repo["existing_review"]:
            continue
        path = pathlib.Path(repo["directory"])
        if not (path / "delivered.txt").exists():
            (path / "delivered.txt").write_text("Delivered")
            subprocess.run(["git", "add", "delivered.txt"], cwd=path, check=True)
            subprocess.run(["git", "commit", "-m", "Deliver"], cwd=path, check=True)
        subprocess.run(repo["publish"], shell=True, check=True)
"""
                for cli in ("codex", "claude"):
                    executable = bin_dir / cli
                    executable.write_text(agent_script)
                    executable.chmod(0o755)
                for cli, link in (("gh", "https://github.com/org/api/pull/1"),
                                  ("glab", "https://gitlab.com/org/web/-/merge_requests/1")):
                    executable = bin_dir / cli
                    executable.write_text(f"""#!{sys.executable}
import pathlib, sys
if sys.argv[2] == "list":
    print("[]")
elif {cli!r} == "glab" and not pathlib.Path({str(root / 'gitlab-ready')!r}).exists():
    sys.exit(2)
else:
    print({link!r})
""")
                    executable.chmod(0o755)

                with patch.dict(os.environ, environment):
                    item = platform.requirement(workspace, "Full flow", "Implement", "codex", target_ids)
                    self.assertTrue(platform.execute_next())
                    self.assertIn("Modify both", platform.get(item)["plan"])
                    platform.approve(item)
                    self.assertTrue(platform.execute_next())
                    partial = platform.get(item)
                    self.assertEqual(partial["status"], "failed_execute", partial["log"])
                    self.assertIsNotNone(partial["targets"][0]["review"])
                    (root / "gitlab-ready").touch()
                    platform.retry(item)
                    self.assertTrue(platform.execute_next())
                    complete = platform.get(item)
                    self.assertEqual(complete["status"], "delivered", complete["log"])
                    self.assertTrue(all(repo["review"] for repo in complete["targets"]))

                    second = platform.requirement(workspace, "Second", "Implement", "claude", target_ids)
                    self.assertTrue(platform.execute_next())
                    platform.approve(second)
                    self.assertTrue(platform.execute_next())
                    self.assertEqual(platform.get(second)["status"], "delivered", platform.get(second)["log"])
                    self.assertFalse(platform.execute_next())
