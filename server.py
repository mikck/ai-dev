"""Serve the console and its API from a single origin."""
import argparse
import json
import os
import secrets
import re
import sqlite3
from http import HTTPStatus
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import urlsplit

from workflow import WorkflowPlatform

PUBLIC = Path(__file__).resolve().parent / "public"


def handler_for(platform, executor_token=None):
    class Handler(BaseHTTPRequestHandler):
        def respond(self, status, body, content_type):
            self.send_response(status)
            self.send_header("Content-Type", content_type)
            self.send_header("Content-Length", str(len(body)))
            self.send_header("X-Content-Type-Options", "nosniff")
            self.end_headers()
            self.wfile.write(body)

        def api(self, status, result):
            self.respond(status, json.dumps(result, ensure_ascii=False).encode(), "application/json")

        def do_GET(self):
            route = urlsplit(self.path).path
            if route == "/api":
                self.api(HTTPStatus.OK, platform.list())
                return
            if route == "/api/executors":
                self.api(HTTPStatus.OK, platform.executors())
                return
            setup = re.fullmatch(r"/api/workspaces/([a-f0-9]{32})/setup", route)
            if setup:
                self.api(HTTPStatus.OK, platform.workspace_setup(setup[1]))
                return
            draft = re.fullmatch(r"/api/drafts/([a-f0-9]{32})", route)
            if draft:
                try:
                    self.api(HTTPStatus.OK, platform.get_draft(draft[1]))
                except ValueError as error:
                    self.api(HTTPStatus.NOT_FOUND, {"error": str(error)})
                return
            match = re.fullmatch(r"/api/requirements/([a-f0-9]{32})", route)
            if match:
                try:
                    self.api(HTTPStatus.OK, platform.get(match[1]))
                except ValueError as error:
                    self.api(HTTPStatus.NOT_FOUND, {"error": str(error)})
                return
            assets = {"/": ("index.html", "text/html; charset=utf-8"),
                      "/app.js": ("app.js", "text/javascript"),
                      "/app.css": ("app.css", "text/css")}
            if route not in assets:
                self.send_error(HTTPStatus.NOT_FOUND)
                return
            filename, media = assets[route]
            try:
                self.respond(HTTPStatus.OK, (PUBLIC / filename).read_bytes(), media)
            except FileNotFoundError:
                self.send_error(HTTPStatus.NOT_FOUND, "Build frontend with npm run build")

        def do_POST(self):
            route = urlsplit(self.path).path
            actions = {"/api/workspaces": platform.workspace,
                       "/api/repositories": platform.repository,
                       "/api/requirements": platform.requirement,
                       "/api/resources": platform.resource,
                       "/api/resources/delete": platform.delete_resource,
                       "/api/sops": platform.save_sop,
                       "/api/sops/delete": platform.delete_sop,
                       "/api/drafts": platform.draft,
                       "/api/executors/manage": platform.set_executor,
                       "/api/executors/recover": platform.recover_executor}
            action = re.fullmatch(r"/api/requirements/([a-f0-9]{32})/(approve|retry|review)", route)
            remote = re.fullmatch(r"/api/worker/(register|claim|heartbeat|complete)", route)
            if route not in actions and action is None and remote is None:
                self.send_error(HTTPStatus.NOT_FOUND)
                return
            if self.headers.get_content_type() != "application/json":
                self.send_error(HTTPStatus.UNSUPPORTED_MEDIA_TYPE)
                return
            try:
                size = int(self.headers.get("Content-Length", "0"))
                if not 0 < size <= 524288:
                    raise ValueError("请求大小无效")
                payload = json.loads(self.rfile.read(size))
                if remote:
                    provided = self.headers.get("Authorization", "")
                    if not executor_token or not secrets.compare_digest(provided, "Bearer " + executor_token):
                        self.api(HTTPStatus.UNAUTHORIZED, {"error": "执行器访问令牌无效"})
                        return
                    identifier = payload.pop("identifier")
                    proof = payload.pop("proof")
                    if remote[1] == "register":
                        if not isinstance(proof, str) or not re.fullmatch(r"[a-f0-9]{64}", proof):
                            raise ValueError("执行器凭据格式无效")
                        platform.register(identifier, proof=proof, **payload)
                        self.api(HTTPStatus.OK, {"id": identifier})
                        return
                    if not platform.verify_executor(identifier, proof):
                        self.api(HTTPStatus.UNAUTHORIZED, {"error": "执行器凭据无效"})
                        return
                    if remote[1] == "claim":
                        self.api(HTTPStatus.OK, {"job": platform.claim_remote(identifier)})
                        return
                    if remote[1] == "heartbeat":
                        platform.heartbeat(identifier)
                    else:
                        platform.complete_remote(identifier, **payload)
                    self.api(HTTPStatus.OK, {"id": identifier})
                elif action:
                    if action[2] == "review":
                        platform.review_node(action[1], **payload)
                    else:
                        method = platform.approve if action[2] == "approve" else platform.retry
                        method(action[1])
                    self.api(HTTPStatus.OK, {"id": action[1]})
                else:
                    self.api(HTTPStatus.CREATED, {"id": actions[route](**payload)})
            except (ValueError, TypeError, KeyError, AttributeError, sqlite3.IntegrityError) as error:
                self.api(HTTPStatus.BAD_REQUEST, {"error": str(error)})

    return Handler


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", type=Path, default=Path(".delivery"))
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=8000)
    args = parser.parse_args()
    with ThreadingHTTPServer((args.host, args.port), handler_for(WorkflowPlatform(args.root), os.environ.get("DELIVERY_EXECUTOR_TOKEN"))) as server:
        server.serve_forever()
