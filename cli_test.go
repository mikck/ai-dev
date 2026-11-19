package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPackagedCLI(t *testing.T) {
	binary := os.Getenv("AIDEV_TEST_BINARY")
	if binary == "" {
		t.Skip("run npm test to also exercise the packaged binary")
	}
	root := t.TempDir()
	env := append(os.Environ(), "DELIVERY_EXECUTOR_TOKEN=integration-token",
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir, cmd.Env = dir, env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	binDir := filepath.Join(root, "fake-tools")
	if err := os.Mkdir(binDir, 0700); err != nil {
		t.Fatal(err)
	}
	env = append(env, "PATH="+binDir+":"+os.Getenv("PATH"))
	providers := []string{"github", "gitlab"}
	repositories := []string{"https://github.com/example/api.git", "https://gitlab.com/example/web.git"}
	for i, remote := range repositories {
		repo := filepath.Join(root, fmt.Sprintf("repo-%d", i))
		run(root, "git", "init", "-b", "main", repo)
		if err := os.WriteFile(filepath.Join(repo, "README"), []byte("initial"), 0600); err != nil {
			t.Fatal(err)
		}
		run(repo, "git", "add", "README")
		run(repo, "git", "commit", "-m", "initial")
		env = append(env, fmt.Sprintf("GIT_CONFIG_KEY_%d=url.%s.insteadOf", i, repo), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, remote))
	}
	env = append(env, "GIT_CONFIG_COUNT=2")
	// Re-run this test binary as a fixture CLI, replacing argv[0] through shell wrappers.
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"codex", "claude", "gh", "glab"} {
		script := "#!/bin/sh\nexport AIDEV_FIXTURE=" + name + "\nexec " + shellJoin(self, "-test.run=^TestFixtureCLI$") + " -- \"$@\"\n"
		if err := os.WriteFile(filepath.Join(binDir, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := fmt.Sprint(listener.Addr().(*net.TCPAddr).Port)
	listener.Close()
	base := "http://127.0.0.1:" + port
	start := func(args ...string) {
		t.Helper()
		logFile, err := os.CreateTemp(root, "process-")
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(binary, args...)
		cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = root, env, logFile, logFile
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = cmd.Process.Signal(os.Interrupt)
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				_ = cmd.Process.Kill()
				<-done
			}
			logFile.Close()
			if t.Failed() {
				data, _ := os.ReadFile(logFile.Name())
				t.Logf("%s: %s", args[0], data)
			}
		})
	}
	start("serve", "-port", port)
	client := &http.Client{Timeout: 3 * time.Second}
	request := func(method, path string, payload input) (*http.Response, error) {
		req, err := http.NewRequest(method, base+path, strings.NewReader(toJSON(payload)))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		return client.Do(req)
	}
	poll := func(check func() bool) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if check() {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatal("timed out waiting for application")
	}
	poll(func() bool {
		res, err := request("GET", "/", nil)
		if err != nil {
			return false
		}
		res.Body.Close()
		return res.StatusCode == 200
	})
	for _, asset := range []string{"/app.js", "/app.css"} {
		response, err := request("GET", asset, nil)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 200 || response.ContentLength < 1000 {
			t.Fatalf("embedded asset %s: %v", asset, response.StatusCode)
		}
	}
	api := func(method, path string, payload input) input {
		t.Helper()
		response, err := request(method, path, payload)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var data input
		if err := json.NewDecoder(response.Body).Decode(&data); err != nil {
			t.Fatal(err)
		}
		if response.StatusCode >= 400 {
			t.Fatalf("%s %s: %v", method, path, data)
		}
		return data
	}
	workspace := str(api("POST", "/api/workspaces", input{"name": "Integration"}), "id")
	repoIDs := []any{}
	for i, provider := range providers {
		repoIDs = append(repoIDs, str(api("POST", "/api/repositories", input{"workspace": workspace, "provider": provider,
			"url": repositories[i], "base": "main", "verify": "test -f artifact.txt"}), "id"))
	}
	agent := str(api("POST", "/api/resources", input{"workspace": workspace, "kind": "agent", "name": "Builder", "config": input{"engine": "codex"}}), "id")
	sop := str(api("POST", "/api/sops", input{"workspace": workspace, "name": "Delivery", "nodes": []any{
		input{"id": "build", "name": "Build", "type": "agent", "agent": agent, "artifacts": []any{"artifact.txt"}},
		input{"id": "review", "name": "Review", "type": "review", "depends": []any{"build"}},
		input{"id": "publish", "name": "Publish", "type": "agent", "agent": agent, "depends": []any{"review"}, "publish": true},
	}}), "id")
	api("POST", "/api/sops/publish", input{"workspace": workspace, "identifier": sop})
	id := str(api("POST", "/api/requirements", input{"workspace": workspace, "title": "Deliver", "description": "Build both repositories",
		"repositories": repoIDs, "sop": sop}), "id")
	start("executor", "-url", base, "-id", "fixture")
	path := "/api/requirements/" + id
	awaitStatus := func(wanted string) {
		t.Helper()
		poll(func() bool {
			data := api("GET", path, nil)
			status := str(data, "status")
			if status == "sop_failed" {
				t.Fatalf("execution failed: %v", data["nodes"])
			}
			return status == wanted
		})
	}
	awaitStatus("sop_review")
	queued := api("GET", "/api/requirements/"+id, nil)
	build := obj(array(queued["nodes"])[0])
	if len(array(obj(build["artifact"])["files"])) != 2 {
		t.Fatalf("missing declared artifacts: %v", build)
	}
	api("POST", path+"/review", input{"decision": "returned", "node": "review", "target": "build", "note": "Fix the test"})
	awaitStatus("sop_review")
	retried := obj(array(api("GET", path, nil)["nodes"])[0])
	if len(array(retried["attempts"])) != 2 {
		t.Fatalf("missing retry history: %v", retried)
	}
	api("POST", path+"/review", input{"decision": "approved", "note": "Looks good", "node": "review"})
	awaitStatus("delivered")
	data := api("GET", path, nil)
	for i, value := range array(data["targets"]) {
		if !validReview(obj(value), str(obj(value), "review")) {
			t.Fatalf("missing review: %v", value)
		}
		checkout := filepath.Join(root, ".executor", "jobs", id, "worktrees", repoIDs[i].(string))
		if stat, err := os.Stat(filepath.Join(checkout, ".git")); err != nil || stat.IsDir() {
			t.Fatalf("expected linked worktree at %s", checkout)
		}
		if branch := run(checkout, "git", "branch", "--show-current"); branch != "delivery/"+id {
			t.Fatalf("unexpected branch: %s", branch)
		}
		if got := run(checkout, "git", "log", "-3", "--format=%s"); got != "Publish\nBuild\nBuild" {
			t.Fatalf("unexpected commits: %s", got)
		}
	}
	draft := str(api("POST", "/api/drafts", input{"workspace": workspace, "engine": "claude",
		"messages": []any{input{"role": "user", "content": "Build and review"}}}), "id")
	poll(func() bool {
		got := api("GET", "/api/drafts/"+draft, nil)
		if str(got, "status") == "failed" {
			t.Fatalf("draft failed: %v", got)
		}
		return str(got, "status") == "done" && len(array(got["nodes"])) == 1
	})
}

func TestFixtureCLI(t *testing.T) {
	name := os.Getenv("AIDEV_FIXTURE")
	if name == "" {
		t.Skip("fixture process only")
	}
	if name == "gh" || name == "glab" {
		for _, arg := range os.Args {
			if arg == "list" {
				fmt.Println("[]")
				os.Exit(0)
			}
		}
		if name == "gh" {
			fmt.Println("https://github.com/example/api/pull/1")
		} else {
			fmt.Println("https://gitlab.com/example/web/-/merge_requests/1")
		}
		os.Exit(0)
	}
	if name != "codex" && name != "claude" {
		os.Exit(2)
	}
	data, err := os.ReadFile("/dev/stdin")
	if err != nil {
		t.Fatal(err)
	}
	_, body, _ := strings.Cut(string(data), "\n")
	value := obj(fromJSON(body))
	if agents := array(value["agents"]); len(agents) > 0 {
		fmt.Println(toJSON(input{"nodes": []any{input{"id": "build", "name": "Build", "type": "agent", "agent": str(obj(agents[0]), "id")}}}))
		os.Exit(0)
	}
	for _, entry := range array(value["repositories"]) {
		repo := obj(entry)
		path := str(repo, "worktree")
		node := str(value, "node")
		for _, script := range []string{str(repo, "create_worktree"), "printf '%s\\n' " + shellJoin(node) + " >> artifact.txt",
			"git add artifact.txt", "git commit -m " + shellJoin(node), str(repo, "publish")} {
			if script == "" {
				continue
			}
			dir := path
			if script == str(repo, "create_worktree") {
				dir = ""
			}
			if _, err := command(context.Background(), dir, "", "sh", "-ec", script); err != nil {
				t.Fatal(err)
			}
		}
	}
	fmt.Println("fixture complete")
	os.Exit(0)
}

func TestWorkerAuthentication(t *testing.T) {
	s, err := openStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/worker/register", strings.NewReader(
		toJSON(input{"identifier": "worker", "name": "Worker", "proof": strings.Repeat("a", 64)})))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router(s, "test-token").ServeHTTP(recorder, request)
	if recorder.Code != 401 {
		t.Fatalf("expected 401, got %d", recorder.Code)
	}
}
