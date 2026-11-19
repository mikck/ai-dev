package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

const commandTimeout = 30 * time.Minute

type tailBuffer struct {
	data  []byte
	limit int
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if n >= b.limit {
		b.data = append(b.data[:0], p[n-b.limit:]...)
		return n, nil
	}
	if overflow := len(b.data) + n - b.limit; overflow > 0 {
		b.data = append(b.data[:0], b.data[overflow:]...)
	}
	b.data = append(b.data, p...)
	return n, nil
}
func (b *tailBuffer) String() string { return strings.TrimSpace(string(b.data)) }

func command(ctx context.Context, dir, prompt string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir, cmd.Stdin = dir, strings.NewReader(prompt)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 5 * time.Second
	out, stderr := &tailBuffer{limit: 512 * 1024}, &tailBuffer{limit: 10 * 1024}
	cmd.Stdout, cmd.Stderr = out, stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return out.String(), ctx.Err()
		}
		return out.String(), fmt.Errorf("%s: %w: %s", args[0], err, stderr)
	}
	return out.String(), nil
}

func shellJoin(args ...string) string {
	for i, v := range args {
		args[i] = "'" + strings.ReplaceAll(v, "'", "'\"'\"'") + "'"
	}
	return strings.Join(args, " ")
}

func saveJSON(path string, value any) error {
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, ".write-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	err = json.NewEncoder(file).Encode(value)
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), path)
}

func readJSON(path string) (input, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var value input
	err = json.Unmarshal(data, &value)
	return value, err
}

func boundedString(value string, n int) string {
	if len(value) <= n {
		return value
	}
	return value[len(value)-n:]
}

type remote struct {
	base   string
	id     string
	proof  string
	token  string
	client *http.Client
}

type responseError struct {
	status  int
	message string
}

func (e responseError) Error() string {
	return fmt.Sprintf("management HTTP %d: %s", e.status, e.message)
}

func newRemote(address, id, proof, secret string) (*remote, error) {
	u, err := url.Parse(address)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("管理面 URL 无效")
	}
	hostname := u.Hostname()
	local := hostname == "127.0.0.1" || hostname == "::1" || hostname == "localhost"
	if u.Scheme != "https" && !(u.Scheme == "http" && local) {
		return nil, errors.New("远程管理面需要 HTTPS")
	}
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &remote{strings.TrimRight(address, "/"), id, proof, secret, client}, nil
}

func (r *remote) call(ctx context.Context, action string, payload input) (input, error) {
	data := input{"identifier": r.id, "proof": r.proof}
	for k, v := range payload {
		data[k] = v
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.base+"/api/worker/"+action, strings.NewReader(toJSON(data)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	req.Header.Set("Content-Type", "application/json")
	res, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	reader := io.LimitReader(res.Body, 8*1024*1024)
	if res.StatusCode != http.StatusOK {
		buf, _ := io.ReadAll(io.LimitReader(reader, 1024))
		return nil, responseError{res.StatusCode, strings.TrimSpace(string(buf))}
	}
	var response input
	err = json.NewDecoder(reader).Decode(&response)
	return response, err
}

func wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func runExecutor(ctx context.Context, root, address, id, name, secret string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(root, "executor.lock"), os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("执行器目录已被使用: %w", err)
	}
	path := filepath.Join(root, "executor.json")
	identity, err := readJSON(path)
	if errors.Is(err, os.ErrNotExist) {
		identity = input{"id": id, "proof": token(32)}
		if err := saveJSON(path, identity); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if str(identity, "id") != id || !proofPattern.MatchString(str(identity, "proof")) {
		return errors.New("执行器数据目录绑定了其他身份或凭据无效")
	}
	control, err := newRemote(address, id, str(identity, "proof"), secret)
	if err != nil {
		return err
	}
	if _, err := control.call(ctx, "register", input{"name": name}); err != nil {
		return err
	}
	log.Printf("Executor %s registered with %s", id, control.base)
	alive, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		for wait(alive, 10*time.Second) == nil {
			if _, err := control.call(alive, "heartbeat", nil); err != nil && alive.Err() == nil {
				log.Printf("Heartbeat: %v", err)
			}
		}
	}()
	pending := filepath.Join(root, "pending-report.json")
	for ctx.Err() == nil {
		err = executorCycle(ctx, root, pending, control)
		var httpErr responseError
		if errors.As(err, &httpErr) && httpErr.status < 500 {
			return err
		}
		if err != nil && ctx.Err() == nil {
			log.Printf("Executor retry: %v", err)
			if err = wait(ctx, 5*time.Second); err != nil {
				return err
			}
		}
	}
	return ctx.Err()
}

func executorCycle(ctx context.Context, root, pending string, control *remote) error {
	report, err := readJSON(pending)
	if err == nil {
		if _, err := control.call(ctx, "complete", report); err != nil {
			return err
		}
		if err := os.Remove(pending); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	reply, err := control.call(ctx, "claim", nil)
	if err != nil {
		return err
	}
	job := obj(reply["job"])
	if job == nil {
		return wait(ctx, 2*time.Second)
	}
	artifact := input{}
	failure := processJob(ctx, root, job, artifact)
	message := ""
	if failure != nil {
		message = boundedString(failure.Error(), 10000)
	}
	return saveJSON(pending, input{"lease": job["lease"], "artifact": artifact, "error": message})
}

func engineCommand(engine string, planning bool, dir string, mcps []any) ([]string, error) {
	switch engine {
	case "codex":
		mode := "workspace-write"
		if planning {
			mode = "read-only"
		}
		args := []string{"codex", "-a", "never", "exec", "--skip-git-repo-check", "--sandbox", mode}
		if !planning {
			args = append(args, "-c", "sandbox_workspace_write.network_access=true")
		}
		for _, entry := range mcps {
			mcp := obj(entry)
			prefix := "mcp_servers.s_" + str(mcp, "id")
			config := obj(mcp["config"])
			values, _ := stringsOf(config["args"])
			args = append(args, "-c", prefix+".command="+toJSON(str(config, "command")), "-c", prefix+".args="+toJSON(values))
		}
		return append(args, "-"), nil
	case "claude":
		mode := "acceptEdits"
		if planning {
			mode = "plan"
		}
		args := []string{"claude", "-p", "--permission-mode", mode}
		if !planning {
			args = append(args, "--allowedTools", "Bash")
		}
		config := input{}
		for _, entry := range mcps {
			mcp := obj(entry)
			values := obj(mcp["config"])
			parameters, _ := stringsOf(values["args"])
			config["s_"+str(mcp, "id")] = input{"command": str(values, "command"), "args": parameters}
		}
		path := filepath.Join(dir, "mcp.json")
		if err := saveJSON(path, input{"mcpServers": config}); err != nil {
			return nil, err
		}
		return append(args, "--strict-mcp-config", "--mcp-config", path), nil
	default:
		return nil, errors.New("未知执行工具")
	}
}

var codeFence = regexp.MustCompile("(?s)^```(?:json)?\\s*(.*?)\\s*```$")

func processJob(ctx context.Context, root string, job, artifact input) error {
	kind := str(job, "kind")
	if kind == "draft" {
		if !identifierPattern.MatchString(str(job, "id")) {
			return errors.New("草稿 ID 无效")
		}
		dir := filepath.Join(root, "drafts", str(job, "id"))
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		cmd, err := engineCommand(str(job, "engine"), true, dir, nil)
		if err != nil {
			return err
		}
		prompt := "结合对话生成 SOP。只输出 JSON 对象，包含 nodes 数组。节点字段为 id、name、type（agent 或 review）、depends、prompt。Agent 节点另有 agent（可用的 Agent ID）、publish（布尔值）和 artifacts（仓库相对路径数组）。\n"
		output, err := command(ctx, dir, prompt+toJSON(input{"agents": job["agents"], "messages": job["messages"]}), cmd...)
		if err != nil {
			return err
		}
		output = codeFence.ReplaceAllString(output, "$1")
		value := obj(fromJSON(output))
		if value == nil {
			return errors.New("AI 未返回有效的 SOP JSON")
		}
		artifact["nodes"] = value["nodes"]
		return nil
	}
	if kind != "node" && kind != "legacy_plan" && kind != "legacy_execute" {
		return errors.New("未知执行任务")
	}
	return executeNode(ctx, root, job, artifact)
}

func branchFor(item input) string { return "delivery/" + str(item, "id") }

func prepareRepo(ctx context.Context, dir string, repo input) (string, string, error) {
	id := str(repo, "id")
	if !identifierPattern.MatchString(id) {
		return "", "", errors.New("仓库 ID 无效")
	}
	metadata := filepath.Join(dir, "git", id)
	worktree := filepath.Join(dir, "worktrees", id)
	for _, path := range []string{filepath.Dir(metadata), filepath.Dir(worktree)} {
		if err := os.MkdirAll(path, 0700); err != nil {
			return "", "", err
		}
	}
	if _, err := os.Stat(metadata); errors.Is(err, os.ErrNotExist) {
		temp, err := os.MkdirTemp(filepath.Dir(metadata), ".clone-")
		if err != nil {
			return "", "", err
		}
		defer os.RemoveAll(temp)
		base := str(repo, "base")
		if _, err := command(ctx, dir, "", "git", "clone", "--bare", "--branch", base, "--", str(repo, "url"), temp); err != nil {
			return "", "", err
		}
		if _, err := command(ctx, temp, "", "git", "update-ref", "refs/remotes/origin/"+base, "refs/heads/"+base); err != nil {
			return "", "", err
		}
		if err := os.Rename(temp, metadata); err != nil {
			return "", "", err
		}
	} else if err != nil {
		return "", "", err
	}
	return metadata, worktree, nil
}

func executeNode(ctx context.Context, root string, job, artifact input) error {
	item, node := obj(job["item"]), obj(job["node"])
	id, kind := str(item, "id"), str(job, "kind")
	if !identifierPattern.MatchString(id) {
		return errors.New("需求 ID 无效")
	}
	planning := kind == "legacy_plan"
	legacyPublish := kind == "legacy_execute"
	publishing := legacyPublish || flagValue(node, "publish")
	runtime := obj(node["runtime"])
	engine := str(runtime, "engine")
	if kind != "node" {
		engine = str(item, "engine")
	}
	dir := filepath.Join(root, "jobs", id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	manifest := filepath.Join(dir, "execution.json")
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	repositories := []input{}
	commits, reviews := input{}, input{}
	artifact["commits"], artifact["reviews"] = commits, reviews
	targets := array(item["targets"])
	branch := branchFor(item)
	for _, entry := range targets {
		repo := obj(entry)
		metadata, worktree, err := prepareRepo(ctx, dir, repo)
		if err != nil {
			return err
		}
		row := input{"url": repo["url"], "git_dir": metadata, "target": repo["base"], "branch": branch, "verify": repo["verify"], "review": repo["review"]}
		if !planning {
			row["worktree"] = worktree
			if _, err := os.Stat(worktree); errors.Is(err, os.ErrNotExist) {
				row["create_worktree"] = shellJoin("git", "--git-dir", metadata, "worktree", "add", "-b", branch, worktree, "refs/heads/"+str(repo, "base"))
			} else if err != nil {
				return err
			}
		}
		if publishing {
			row["publish"] = shellJoin(executable, "publish", manifest, str(repo, "id"))
		}
		repositories = append(repositories, row)
	}
	if err := saveJSON(manifest, input{"item": item, "publish": publishing}); err != nil {
		return err
	}
	defer os.Remove(manifest)
	previous := []input{}
	for _, value := range array(item["nodes"]) {
		step := obj(value)
		if str(step, "status") == "done" {
			summary := input{}
			for k, v := range obj(step["artifact"]) {
				if k != "files" {
					summary[k] = v
				}
			}
			previous = append(previous, input{"name": step["name"], "artifact": summary})
		}
	}
	skills := []string{}
	for _, value := range array(runtime["skills"]) {
		skills = append(skills, str(obj(obj(value)["config"]), "content"))
	}
	prompt := "执行 SOP 节点。先对提供 create_worktree 的仓库执行该命令，由你创建 Git worktree。复用已存在的工作目录和需求分支，完成后提交并验证。为审核生成 artifact_paths 中声明的文件，并根据 review_feedback 返工。对有变更且提供 publish 的仓库运行该命令。\n"
	if planning {
		prompt = "只读分析 Git 仓库，用 git --git-dir 查看内容，输出改动和验证计划。\n"
	}
	contextData := input{"requirement": item["title"], "description": item["description"], "node": node["name"],
		"instructions": node["prompt"], "agent": runtime["prompt"], "skills": skills, "approved_plan": item["plan"],
		"previous_artifacts": previous, "repositories": repositories, "review_feedback": job["feedback"], "artifact_paths": node["artifacts"]}
	cmd, err := engineCommand(engine, planning, dir, array(runtime["mcps"]))
	if err != nil {
		return err
	}
	output, runErr := command(ctx, dir, prompt+toJSON(contextData), cmd...)
	artifact["output"] = boundedString(output, 12000)
	if planning {
		return runErr
	}
	firstError := runErr
	for _, entry := range targets {
		repo := obj(entry)
		repoID := str(repo, "id")
		worktree := filepath.Join(dir, "worktrees", repoID)
		if b, err := os.ReadFile(filepath.Join(dir, "worktrees", repoID+".review")); err == nil {
			reviews[repoID] = string(b)
		}
		commit, err := verifyWorktree(ctx, dir, worktree, branch, repo, publishing, str(reviews, repoID))
		if err != nil && firstError == nil {
			firstError = err
		}
		if commit != "" {
			commits[repoID] = commit
		}
	}
	paths, err := artifactPaths(node["artifacts"])
	if err != nil {
		return err
	}
	if len(paths) > 0 {
		files, err := collectArtifacts(paths, repositories)
		artifact["files"] = files
		if err != nil && firstError == nil {
			firstError = err
		}
	}
	if legacyPublish && len(reviews) == 0 && firstError == nil {
		firstError = errors.New("AI 未发布任何 PR 或 MR")
	}
	return firstError
}

func verifyWorktree(ctx context.Context, dir, worktree, branch string, repo input, publishing bool, review string) (string, error) {
	common, err := command(ctx, worktree, "", "git", "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || filepath.Clean(common) != filepath.Join(dir, "git", str(repo, "id")) {
		return "", fmt.Errorf("Agent 未创建所需 Git worktree: %s", str(repo, "url"))
	}
	commit, err := command(ctx, worktree, "", "git", "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	if err := checkWorkingTree(ctx, worktree, branch); err != nil {
		return commit, err
	}
	if publishing {
		diff, err := command(ctx, worktree, "", "git", "diff", "--name-only", "origin/"+str(repo, "base")+"...HEAD")
		if err != nil {
			return commit, err
		}
		if diff != "" {
			if !validReview(repo, review) {
				return commit, fmt.Errorf("未创建审核请求: %s", str(repo, "url"))
			}
			pushed, err := command(ctx, worktree, "", "git", "rev-parse", "origin/"+branch)
			if err != nil {
				return commit, err
			}
			if pushed != commit {
				return commit, fmt.Errorf("仓库有发布后的新提交: %s", str(repo, "url"))
			}
		}
	}
	return commit, nil
}

func checkWorkingTree(ctx context.Context, worktree, branch string) error {
	active, err := command(ctx, worktree, "", "git", "branch", "--show-current")
	if err != nil {
		return err
	}
	if active != branch {
		return errors.New("当前 Git 分支与需求分支不符")
	}
	dirty, err := command(ctx, worktree, "", "git", "status", "--porcelain")
	if err != nil {
		return err
	}
	if dirty != "" {
		return errors.New("请提交工作目录中的变更")
	}
	return nil
}
