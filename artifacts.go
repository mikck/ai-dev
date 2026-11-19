package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"
)

const artifactFileLimit = 16 * 1024
const artifactTotalLimit = 96 * 1024
const artifactCountLimit = 24

func artifactPaths(value any) ([]string, error) {
	paths, ok := stringsOf(value)
	if !ok || len(paths) > 8 {
		return nil, errors.New("最多声明 8 个文本产物路径")
	}
	seen := map[string]bool{}
	for _, path := range paths {
		if path == "." || !fs.ValidPath(path) || len(path) > 240 || strings.Contains(path, "\\") ||
			strings.Contains("/"+path+"/", "/.git/") || strings.IndexFunc(path, unicode.IsControl) >= 0 || seen[path] {
			return nil, errors.New("产物须使用互不重复的仓库相对路径")
		}
		seen[path] = true
	}
	return paths, nil
}

func artifactText(worktree, path string) (string, error) {
	root, err := os.OpenRoot(worktree)
	if err != nil {
		return "", err
	}
	defer root.Close()
	stat, err := root.Stat(path)
	if err != nil {
		return "", err
	}
	if !stat.Mode().IsRegular() || stat.Size() > artifactFileLimit {
		return "", errors.New("产物必须是 16 KiB 内的文本文件")
	}
	file, err := root.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("产物必须是文本文件")
	}
	data, err := io.ReadAll(io.LimitReader(file, artifactFileLimit+1))
	if err != nil {
		return "", err
	}
	if len(data) > artifactFileLimit || !utf8.Valid(data) || strings.ContainsRune(string(data), 0) {
		return "", errors.New("产物必须是 16 KiB 内的 UTF-8 文本")
	}
	return string(data), nil
}

func collectArtifacts(paths []string, repositories []input) ([]input, error) {
	files := []input{}
	size := 0
	for _, path := range paths {
		found := false
		for _, repo := range repositories {
			text, err := artifactText(str(repo, "worktree"), path)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return files, fmt.Errorf("读取产物 %s: %w", path, err)
			}
			size += len(toJSON(text))
			if size > artifactTotalLimit || len(files) >= artifactCountLimit {
				return files, errors.New("单次节点产物超过 24 项或 96 KiB")
			}
			files = append(files, input{"repository": repo["url"], "path": path, "content": text})
			found = true
		}
		if !found {
			return files, fmt.Errorf("未找到声明的产物 %s", path)
		}
	}
	return files, nil
}
