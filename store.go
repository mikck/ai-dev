package main

import (
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

//go:embed schema.sql
var schema string

type input = map[string]any
type store struct{ db *gorm.DB }

var hosts = map[string]string{"github": "github.com", "gitlab": "gitlab.com"}
var identifierPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
var keyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)
var projectPattern = regexp.MustCompile(`^(?:/[A-Za-z0-9_.-]+){2,}$`)

func token(size int) string {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func str(data input, key string) string {
	value, _ := data[key].(string)
	return value
}

func obj(value any) input {
	data, _ := value.(map[string]any)
	return data
}

func array(value any) []any {
	data, _ := value.([]any)
	return data
}

func stringsOf(value any) ([]string, bool) {
	if value == nil {
		return []string{}, true
	}
	items, ok := value.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		value, ok := item.(string)
		if !ok {
			return nil, false
		}
		out = append(out, value)
	}
	return out, true
}

func flagValue(data input, key string) bool {
	value, _ := data[key].(bool)
	return value
}

func toJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(data)
}

func fromJSON(value any) any {
	var result any
	var data []byte
	switch value := value.(type) {
	case string:
		data = []byte(value)
	case []byte:
		data = value
	default:
		return nil
	}
	if json.Unmarshal(data, &result) != nil {
		return nil
	}
	return result
}

func engineOK(engine string) bool { return engine == "codex" || engine == "claude" }

func openStore(root string) (*store, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(filepath.Join(root, "delivery.db"))
	if err != nil {
		return nil, err
	}
	dsn := (&url.URL{Scheme: "file", Path: absolute, RawQuery: "_foreign_keys=on&_busy_timeout=5000"}).String()
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.Exec(schema).Error; err != nil {
		sqlDB.Close()
		return nil, err
	}
	if err := migrateReviews(db); err != nil {
		sqlDB.Close()
		return nil, err
	}
	if err := migrateWorkspaceSettings(db); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return &store{db}, nil
}

func rows(db *gorm.DB, query string, args ...any) ([]input, error) {
	result := []input{}
	err := db.Raw(query, args...).Scan(&result).Error
	return result, err
}

func one(db *gorm.DB, query string, args ...any) (input, error) {
	result, err := rows(db, query, args...)
	if err != nil || len(result) == 0 {
		return nil, err
	}
	return result[0], nil
}

func changed(result *gorm.DB, message string) error {
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return errors.New(message)
	}
	return nil
}

func (s *store) workspace(data input) (string, error) {
	name := strings.TrimSpace(str(data, "name"))
	if name == "" {
		return "", errors.New("请输入 workspace 名称")
	}
	id := token(16)
	return id, s.db.Table("workspaces").Create(input{"id": id, "name": name}).Error
}

func validBranch(branch string) bool {
	if branch == "" || strings.HasPrefix(branch, "-") || strings.HasSuffix(branch, ".") ||
		strings.Contains(branch, "..") || strings.Contains(branch, "@{") {
		return false
	}
	for _, part := range strings.Split(branch, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return strings.IndexFunc(branch, func(c rune) bool {
		return c <= ' ' || c == 127 || strings.ContainsRune("\x7e^:?*[\\", c)
	}) == -1
}

func (s *store) repository(data input) (string, error) {
	provider, raw, base := str(data, "provider"), str(data, "url"), str(data, "base")
	u, err := url.Parse(raw)
	if err != nil || hosts[provider] == "" || u.Scheme != "https" || u.Host != hosts[provider] || u.User != nil ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || !projectPattern.MatchString(u.EscapedPath()) ||
		strings.Contains("/"+strings.Trim(u.Path, "/")+"/", "/../") || strings.Contains(u.Path+"/", "/./") {
		return "", errors.New("请输入受支持平台上无凭据的 HTTPS 仓库地址")
	}
	if !validBranch(base) {
		return "", errors.New("目标分支无效")
	}
	return s.saveRepository(str(data, "identifier"), str(data, "workspace"), input{
		"provider": provider, "url": strings.TrimSuffix(raw, ".git") + ".git", "base": base, "verify": str(data, "verify")})
}

func readResources(db *gorm.DB, workspace string) ([]input, error) {
	items, err := rows(db, "SELECT * FROM resources WHERE workspace = ? ORDER BY name", workspace)
	for _, item := range items {
		item["config"] = fromJSON(item["config"])
	}
	return items, err
}

func resourceIndex(db *gorm.DB, workspace string) (map[string]input, error) {
	items, err := readResources(db, workspace)
	result := map[string]input{}
	for _, item := range items {
		result[str(item, "id")] = item
	}
	return result, err
}

func (s *store) resource(data input) (string, error) {
	workspace, kind, name := str(data, "workspace"), str(data, "kind"), strings.TrimSpace(str(data, "name"))
	id := str(data, "identifier")
	config := obj(data["config"])
	if name == "" || config == nil {
		return "", errors.New("资源名称和配置不能为空")
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		switch kind {
		case "agent":
			if !engineOK(str(config, "engine")) {
				return errors.New("请选择 Codex 或 Claude Code")
			}
			available, err := resourceIndex(tx, workspace)
			if err != nil {
				return err
			}
			for field, expected := range map[string]string{"skills": "skill", "mcps": "mcp"} {
				ids, ok := stringsOf(config[field])
				if !ok {
					return errors.New("资源引用必须为 ID 数组")
				}
				for _, ref := range ids {
					if str(available[ref], "kind") != expected {
						return errors.New("Agent 只能引用本 workspace 的资源")
					}
				}
			}
		case "skill":
			if _, ok := config["content"].(string); !ok {
				return errors.New("Skill 需要文本内容")
			}
		case "mcp":
			if _, ok := stringsOf(config["args"]); !ok || strings.TrimSpace(str(config, "command")) == "" {
				return errors.New("MCP 需要 stdio 命令和字符串参数")
			}
		default:
			return errors.New("资源类型无效")
		}
		values := input{"name": name, "config": toJSON(config)}
		if id != "" {
			return changed(tx.Table("resources").Where("id = ? AND workspace = ? AND kind = ?", id, workspace, kind).Updates(values), "资源不存在")
		}
		id = token(16)
		values["id"], values["workspace"], values["kind"] = id, workspace, kind
		return tx.Table("resources").Create(values).Error
	})
	return id, err
}

func (s *store) deleteResource(data input) error {
	workspace, id := str(data, "workspace"), str(data, "identifier")
	return s.db.Transaction(func(tx *gorm.DB) error {
		resources, err := readResources(tx, workspace)
		if err != nil {
			return err
		}
		for _, resource := range resources {
			for _, field := range []string{"skills", "mcps"} {
				refs, _ := stringsOf(obj(resource["config"])[field])
				for _, ref := range refs {
					if ref == id {
						return errors.New("请先移除 Agent 引用")
					}
				}
			}
		}
		sops, err := rows(tx, "SELECT nodes FROM sops WHERE workspace = ? AND id NOT IN (SELECT id FROM archived_sops)", workspace)
		if err != nil {
			return err
		}
		for _, sop := range sops {
			for _, node := range array(fromJSON(sop["nodes"])) {
				if str(obj(node), "agent") == id {
					return errors.New("请先移除 SOP 引用")
				}
			}
		}
		return changed(tx.Exec("DELETE FROM resources WHERE workspace = ? AND id = ?", workspace, id), "资源不存在")
	})
}

func validateNodes(db *gorm.DB, workspace string, value any) ([]input, error) {
	list := array(value)
	if len(list) == 0 || len(list) > 50 {
		return nil, errors.New("SOP 需要 1 至 50 个节点")
	}
	resources, err := resourceIndex(db, workspace)
	if err != nil {
		return nil, err
	}
	nodes := make([]input, 0, len(list))
	all := map[string]bool{}
	for _, entry := range list {
		node := obj(entry)
		id, name, kind := str(node, "id"), strings.TrimSpace(str(node, "name")), str(node, "type")
		deps, ok := stringsOf(node["depends"])
		if !keyPattern.MatchString(id) || name == "" || all[id] || !ok || (kind != "agent" && kind != "review") {
			return nil, errors.New("节点编号、名称、类型或依赖无效")
		}
		normalized := input{"id": id, "name": name, "type": kind, "depends": deps, "prompt": str(node, "prompt")}
		if raw, exists := node["position"]; exists {
			position := obj(raw)
			x, xOK := position["x"].(float64)
			y, yOK := position["y"].(float64)
			if !xOK || !yOK || math.IsNaN(x) || math.IsNaN(y) || math.Abs(x) > 100000 || math.Abs(y) > 100000 {
				return nil, errors.New("画布节点位置无效")
			}
			normalized["position"] = input{"x": x, "y": y}
		}
		if kind == "agent" {
			agent := str(node, "agent")
			if str(resources[agent], "kind") != "agent" {
				return nil, errors.New("请选择本 workspace 的 Agent")
			}
			normalized["agent"], normalized["publish"] = agent, flagValue(node, "publish")
			paths, err := artifactPaths(node["artifacts"])
			if err != nil {
				return nil, err
			}
			normalized["artifacts"] = paths
		}
		all[id] = true
		nodes = append(nodes, normalized)
	}
	visited := map[string]bool{}
	for len(visited) < len(nodes) {
		count := len(visited)
		for _, node := range nodes {
			ready := true
			for _, dep := range node["depends"].([]string) {
				if !visited[dep] {
					ready = false
					break
				}
			}
			if ready {
				visited[str(node, "id")] = true
			}
		}
		if len(visited) == count {
			return nil, errors.New("SOP 依赖存在循环或引用不存在")
		}
	}
	return nodes, nil
}

func (s *store) saveSOP(data input) (string, error) {
	id, workspace, name := str(data, "identifier"), str(data, "workspace"), strings.TrimSpace(str(data, "name"))
	if name == "" {
		return "", errors.New("请输入 SOP 名称")
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		nodes, err := validateNodes(tx, workspace, data["nodes"])
		if err != nil {
			return err
		}
		values := input{"name": name, "nodes": toJSON(nodes)}
		if id != "" {
			return changed(tx.Table("sops").Where("id = ? AND workspace = ? AND id NOT IN (SELECT id FROM archived_sops)", id, workspace).Updates(values), "SOP 不存在")
		}
		id = token(16)
		values["id"], values["workspace"] = id, workspace
		return tx.Table("sops").Create(values).Error
	})
	return id, err
}

func (s *store) setup(workspace string) (input, error) {
	resources, err := readResources(s.db, workspace)
	if err != nil {
		return nil, err
	}
	sops, err := rows(s.db, `SELECT sops.*, (SELECT MAX(version) FROM sop_versions WHERE sop=sops.id) AS published_version,
        (SELECT json_extract(snapshot,'$.name') FROM sop_versions WHERE sop=sops.id ORDER BY version DESC LIMIT 1) AS published_name
        FROM sops WHERE workspace = ? AND id NOT IN (SELECT id FROM archived_sops) ORDER BY name`, workspace)
	for _, sop := range sops {
		sop["nodes"] = fromJSON(sop["nodes"])
		versions, versionErr := rows(s.db, `SELECT version, published, json_extract(snapshot,'$.name') AS name
            FROM sop_versions WHERE sop=? ORDER BY version DESC`, sop["id"])
		if versionErr != nil {
			return nil, versionErr
		}
		sop["versions"] = versions
	}
	return input{"resources": resources, "sops": sops}, err
}

func (s *store) requirement(data input) (string, error) {
	workspace, title, description, engine, sop := str(data, "workspace"), str(data, "title"), str(data, "description"), str(data, "engine"), str(data, "sop")
	repos, ok := stringsOf(data["repositories"])
	if !ok || len(repos) == 0 || strings.TrimSpace(title) == "" || strings.TrimSpace(description) == "" {
		return "", errors.New("请输入标题和说明并选择仓库")
	}
	if engine == "" {
		engine = "codex"
	}
	if !engineOK(engine) {
		return "", errors.New("执行工具无效")
	}
	id := token(16)
	err := s.db.Transaction(func(tx *gorm.DB) error {
		status := "queued_plan"
		var snapshot input
		if sop != "" {
			status = "sop_queued"
			version, err := one(tx, `SELECT snapshot FROM sop_versions JOIN sops ON sops.id=sop_versions.sop
                WHERE sop=? AND sops.workspace=? AND sops.id NOT IN (SELECT id FROM archived_sops) ORDER BY version DESC LIMIT 1`, sop, workspace)
			if err != nil {
				return err
			}
			if version == nil {
				return errors.New("请选择本 workspace 已发布的 SOP")
			}
			snapshot = obj(fromJSON(version["snapshot"]))
		}
		if err := tx.Table("requirements").Create(input{"id": id, "workspace": workspace, "title": title,
			"description": description, "engine": engine, "status": status}).Error; err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, repo := range repos {
			if seen[repo] {
				continue
			}
			seen[repo] = true
			result := tx.Exec("INSERT INTO targets(requirement,repository) SELECT ?,id FROM repositories WHERE id = ? AND workspace = ? AND id NOT IN (SELECT id FROM archived_repositories)", id, repo, workspace)
			if err := changed(result, "需求只能关联所属 workspace 的仓库"); err != nil {
				return err
			}
			if err := tx.Exec(`INSERT INTO target_settings(requirement,repository,provider,url,base,verify)
                SELECT ?,id,provider,url,base,verify FROM repositories WHERE id=?`, id, repo).Error; err != nil {
				return err
			}
		}
		if snapshot != nil {
			if err := tx.Table("runs").Create(input{"requirement": id, "snapshot": toJSON(snapshot)}).Error; err != nil {
				return err
			}
			for _, node := range array(snapshot["nodes"]) {
				if err := tx.Table("steps").Create(input{"requirement": id, "node": str(obj(node), "id"), "status": "pending"}).Error; err != nil {
					return err
				}
			}
			return advance(tx, id, snapshot)
		}
		return nil
	})
	return id, err
}

func itemAt(db *gorm.DB, id string, includeHistory bool) (input, error) {
	item, err := one(db, "SELECT * FROM requirements WHERE id = ?", id)
	if err != nil {
		return nil, err
	}
	if item == nil {
		return nil, errors.New("需求不存在")
	}
	targets, err := taskRepositories(db, id)
	if err != nil {
		return nil, err
	}
	item["targets"] = targets
	run, err := one(db, "SELECT * FROM runs WHERE requirement = ?", id)
	if err != nil || run == nil {
		return item, err
	}
	snapshot := obj(fromJSON(run["snapshot"]))
	stepRows, err := rows(db, "SELECT * FROM steps WHERE requirement = ?", id)
	if err != nil {
		return nil, err
	}
	byID := map[string]input{}
	for _, step := range stepRows {
		byID[str(step, "node")] = step
	}
	history := map[string][]input{}
	if includeHistory {
		attempts, err := rows(db, "SELECT id,node,status,artifact,finished FROM step_attempts WHERE requirement=? ORDER BY id DESC", id)
		if err != nil {
			return nil, err
		}
		for _, attempt := range attempts {
			attempt["artifact"] = fromJSON(attempt["artifact"])
			key := str(attempt, "node")
			history[key] = append(history[key], attempt)
		}
	}
	nodes := []input{}
	for _, value := range array(snapshot["nodes"]) {
		node := obj(value)
		step := byID[str(node, "id")]
		detail := input{"id": node["id"], "name": node["name"], "type": node["type"], "prompt": node["prompt"],
			"status": step["status"], "artifact": fromJSON(step["artifact"])}
		if includeHistory {
			detail["attempts"] = history[str(node, "id")]
		}
		if str(step, "status") == "review" {
			item["review_node"] = node["id"]
			parents := upstream(snapshot, str(node, "id"))
			choices := []input{}
			for _, entry := range array(snapshot["nodes"]) {
				candidate := obj(entry)
				key := str(candidate, "id")
				if parents[key] && str(candidate, "type") == "agent" && str(byID[key], "status") == "done" {
					choices = append(choices, input{"id": key, "name": candidate["name"]})
				}
			}
			item["return_options"] = choices
		}
		nodes = append(nodes, detail)
	}
	item["sop"], item["executor"], item["nodes"], item["sop_version"] = snapshot["name"], run["executor"], nodes, snapshot["version"]
	return item, nil
}

func (s *store) item(id string) (input, error) { return itemAt(s.db, id, true) }

func (s *store) overview() (input, error) {
	out := input{}
	for _, table := range []string{"workspaces", "repositories", "requirements"} {
		query := "SELECT * FROM " + table + " ORDER BY rowid DESC"
		if table == "repositories" {
			query = "SELECT * FROM repositories WHERE id NOT IN (SELECT id FROM archived_repositories) ORDER BY rowid DESC"
		}
		if table == "requirements" {
			query = `SELECT requirements.*, json_extract(runs.snapshot,'$.name') AS sop
				FROM requirements LEFT JOIN runs ON runs.requirement = requirements.id ORDER BY requirements.rowid DESC`
		}
		values, err := rows(s.db, query)
		if err != nil {
			return nil, err
		}
		out[table] = values
	}
	return out, nil
}

func advance(db *gorm.DB, id string, snapshot input) error {
	steps, err := rows(db, "SELECT node,status FROM steps WHERE requirement = ?", id)
	if err != nil {
		return err
	}
	states := map[string]string{}
	for _, step := range steps {
		states[str(step, "node")] = str(step, "status")
	}
	status := "delivered"
	unfinished := false
	for _, entry := range array(snapshot["nodes"]) {
		node := obj(entry)
		nodeID := str(node, "id")
		if states[nodeID] == "done" {
			continue
		}
		unfinished = true
		if states[nodeID] != "pending" {
			continue
		}
		ready := true
		deps, _ := stringsOf(node["depends"])
		for _, dep := range deps {
			if states[dep] != "done" {
				ready = false
				break
			}
		}
		if !ready {
			continue
		}
		stepStatus := "ready"
		status = "sop_queued"
		if str(node, "type") == "review" {
			stepStatus, status = "review", "sop_review"
		}
		if err := db.Exec("UPDATE steps SET status = ? WHERE requirement = ? AND node = ?", stepStatus, id, nodeID).Error; err != nil {
			return err
		}
		unfinished = false
		break
	}
	if unfinished {
		return fmt.Errorf("需求 %s 没有可推进的节点", id)
	}
	return db.Exec("UPDATE requirements SET status = ? WHERE id = ?", status, id).Error
}

func (s *store) review(id, decision, note string, expected string) error {
	if decision != "approved" && decision != "rejected" {
		return errors.New("无效的审核决定")
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		status := "done"
		if decision == "rejected" {
			status = "rejected"
		}
		step, err := one(tx, "SELECT node FROM steps WHERE requirement=? AND status='review'", id)
		if err != nil {
			return err
		}
		if step == nil || expected != "" && str(step, "node") != expected {
			return errors.New("待审核节点已变化，请刷新详情")
		}
		artifact := input{"decision": decision, "note": note}
		if err := recordAttempt(tx, id, str(step, "node"), status, artifact); err != nil {
			return err
		}
		if err := tx.Exec("UPDATE steps SET status=?,artifact=? WHERE requirement=? AND node=?",
			status, toJSON(artifact), id, step["node"]).Error; err != nil {
			return err
		}
		if decision == "rejected" {
			return tx.Exec("UPDATE requirements SET status='sop_rejected' WHERE id = ?", id).Error
		}
		run, err := one(tx, "SELECT snapshot FROM runs WHERE requirement = ?", id)
		if err != nil {
			return err
		}
		return advance(tx, id, obj(fromJSON(run["snapshot"])))
	})
}

func (s *store) approve(id string) error {
	return changed(s.db.Exec("UPDATE requirements SET status='queued_execute' WHERE id = ? AND status='awaiting_approval'", id), "没有待批准的计划")
}

func (s *store) retry(id string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		result := tx.Exec("UPDATE steps SET status='ready', lease=NULL WHERE requirement = ? AND status='failed'", id)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected > 0 {
			return tx.Exec("UPDATE requirements SET status='sop_queued' WHERE id = ?", id).Error
		}
		return changed(tx.Exec(`UPDATE requirements SET status = CASE status WHEN 'failed_plan' THEN 'queued_plan'
			ELSE 'queued_execute' END WHERE id = ? AND status IN ('failed_plan','failed_execute')`, id), "没有可以重试的节点")
	})
}
