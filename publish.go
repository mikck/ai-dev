package main

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func projectPath(repo input) string {
	u, err := url.Parse(str(repo, "url"))
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.TrimSuffix(u.Path, ".git"), "/")
}

func validReview(repo input, review string) bool {
	suffix := "/pull/"
	provider := str(repo, "provider")
	if provider == "gitlab" {
		suffix = "/-/merge_requests/"
	}
	host := hosts[provider]
	project := projectPath(repo)
	if host == "" || project == "" {
		return false
	}
	matched, _ := regexp.MatchString("(?i)^"+regexp.QuoteMeta("https://"+host+"/"+project+suffix)+`[0-9]+$`, review)
	return matched
}

func publish(ctx context.Context, manifest, repoID string) (string, error) {
	execution, err := readJSON(manifest)
	if err != nil {
		return "", err
	}
	if !flagValue(execution, "publish") || !identifierPattern.MatchString(repoID) {
		return "", errors.New("当前节点未配置发布，或仓库 ID 无效")
	}
	item := obj(execution["item"])
	var repo input
	for _, entry := range array(item["targets"]) {
		if str(obj(entry), "id") == repoID {
			repo = obj(entry)
			break
		}
	}
	if repo == nil {
		return "", errors.New("仓库不属于当前需求")
	}
	dir := filepath.Join(filepath.Dir(manifest), "worktrees")
	worktree := filepath.Join(dir, repoID)
	branch := branchFor(item)
	if err := checkWorkingTree(ctx, worktree, branch); err != nil {
		return "", err
	}
	base := str(repo, "base")
	diff, err := command(ctx, worktree, "", "git", "diff", "--name-only", "origin/"+base+"...HEAD")
	if err != nil {
		return "", err
	}
	if diff == "" {
		return "", errors.New("需求分支没有可发布的变更")
	}
	if verify := str(repo, "verify"); verify != "" {
		if _, err := command(ctx, worktree, "", "sh", "-ec", verify); err != nil {
			return "", err
		}
	}
	if err := checkWorkingTree(ctx, worktree, branch); err != nil {
		return "", err
	}
	repository := str(repo, "url")
	if _, err := command(ctx, worktree, "", "git", "push", repository, "HEAD:refs/heads/"+branch); err != nil {
		return "", err
	}
	if _, err := command(ctx, worktree, "", "git", "fetch", repository, "refs/heads/"+branch+":refs/remotes/origin/"+branch); err != nil {
		return "", err
	}
	receipt := filepath.Join(dir, repoID+".review")
	prior, err := os.ReadFile(receipt)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	link := string(prior)
	if link == "" {
		link = str(repo, "review")
	}
	if link == "" {
		link, err = createReview(ctx, worktree, item, repo, branch)
		if err != nil {
			return "", err
		}
	}
	if !validReview(repo, link) {
		return "", errors.New("发布工具未返回有效的审核链接")
	}
	file, err := os.CreateTemp(dir, ".review-")
	if err != nil {
		return "", err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.WriteString(link)
	closeErr := file.Close()
	if writeErr != nil {
		return "", writeErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	return link, os.Rename(file.Name(), receipt)
}

func createReview(ctx context.Context, dir string, item, repo input, branch string) (string, error) {
	project, base, title := projectPath(repo), str(repo, "base"), str(item, "title")
	body := "Delivery: " + str(item, "id")
	github := str(repo, "provider") == "github"
	var list, create []string
	field := "url"
	if github {
		list = []string{"gh", "pr", "list", "--repo", project, "--head", branch, "--base", base, "--state", "open", "--json", "url"}
		create = []string{"gh", "pr", "create", "--repo", project, "--head", branch, "--base", base, "--title", title, "--body", body, "--draft"}
	} else {
		field = "web_url"
		list = []string{"glab", "mr", "list", "--repo", project, "--source-branch", branch, "--target-branch", base, "--output", "json"}
		create = []string{"glab", "mr", "create", "--repo", project, "--source-branch", branch, "--target-branch", base,
			"--title", title, "--description", body, "--draft", "--yes"}
	}
	output, err := command(ctx, dir, "", list...)
	if err != nil {
		return "", err
	}
	existing := array(fromJSON(output))
	if existing == nil {
		return "", errors.New("发布工具返回了无效的审核请求列表")
	}
	if len(existing) > 0 {
		return str(obj(existing[0]), field), nil
	}
	output, err = command(ctx, dir, "", create...)
	if err != nil {
		return "", err
	}
	for _, candidate := range strings.Fields(output) {
		if validReview(repo, candidate) {
			return candidate, nil
		}
	}
	return "", errors.New("发布工具未返回审核链接")
}
