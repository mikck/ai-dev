package main

import (
	"errors"
	"strings"

	"gorm.io/gorm"
)

func migrateReviews(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		result := tx.Exec("INSERT OR IGNORE INTO migrations(name) VALUES ('reviews-v1')")
		if result.Error != nil || result.RowsAffected == 0 {
			return result.Error
		}
		drafts, err := rows(tx, "SELECT * FROM sops")
		if err != nil {
			return err
		}
		for _, draft := range drafts {
			if _, err := createVersion(tx, draft); err != nil {
				return err
			}
		}
		return tx.Exec(`INSERT INTO step_attempts(requirement,node,status,artifact,finished)
			SELECT requirement,node,status,artifact,? FROM steps WHERE artifact IS NOT NULL`, now()).Error
	})
}

func createVersion(tx *gorm.DB, draft input) (int, error) {
	resources, err := resourceIndex(tx, str(draft, "workspace"))
	if err != nil {
		return 0, err
	}
	nodes := array(fromJSON(draft["nodes"]))
	for _, value := range nodes {
		node := obj(value)
		if str(node, "type") != "agent" {
			continue
		}
		agent := resources[str(node, "agent")]
		if agent == nil {
			return 0, errors.New("SOP 引用的 Agent 已不存在")
		}
		config := obj(agent["config"])
		runtime := input{"name": str(agent, "name"), "engine": str(config, "engine"), "prompt": str(config, "prompt")}
		for _, field := range []string{"skills", "mcps"} {
			refs, _ := stringsOf(config[field])
			selected := []input{}
			for _, ref := range refs {
				if resources[ref] == nil {
					return 0, errors.New("SOP 引用的资源已不存在")
				}
				selected = append(selected, resources[ref])
			}
			runtime[field] = selected
		}
		node["runtime"] = runtime
	}
	var version int
	if err := tx.Raw("SELECT COALESCE(MAX(version),0)+1 FROM sop_versions WHERE sop=?", draft["id"]).Scan(&version).Error; err != nil {
		return 0, err
	}
	snapshot := input{"name": draft["name"], "nodes": nodes, "version": version}
	return version, tx.Table("sop_versions").Create(input{"sop": draft["id"], "version": version,
		"snapshot": toJSON(snapshot), "published": now()}).Error
}

func (s *store) publishSOP(data input) (int, error) {
	var version int
	err := s.db.Transaction(func(tx *gorm.DB) error {
		draft, err := one(tx, "SELECT * FROM sops WHERE id=? AND workspace=?", str(data, "identifier"), str(data, "workspace"))
		if err != nil {
			return err
		}
		if draft == nil {
			return errors.New("SOP 不存在")
		}
		version, err = createVersion(tx, draft)
		return err
	})
	return version, err
}

func (s *store) reviewInbox(workspace string) ([]input, error) {
	return rows(s.db, `SELECT requirements.id,requirements.title,requirements.workspace,
		json_extract(runs.snapshot,'$.name') AS sop, json_extract(runs.snapshot,'$.version') AS version,
		steps.node,(SELECT json_extract(value,'$.name') FROM json_each(runs.snapshot,'$.nodes')
		WHERE json_extract(value,'$.id')=steps.node) AS node_name
		FROM steps JOIN runs USING(requirement) JOIN requirements ON requirements.id=steps.requirement
		WHERE requirements.workspace=? AND steps.status='review' ORDER BY requirements.rowid DESC`, workspace)
}

func recordAttempt(tx *gorm.DB, id, node, status string, artifact input) error {
	return tx.Table("step_attempts").Create(input{"requirement": id, "node": node, "status": status,
		"artifact": toJSON(artifact), "finished": now()}).Error
}

func upstream(snapshot input, nodeID string) map[string]bool {
	graph := map[string][]string{}
	for _, entry := range array(snapshot["nodes"]) {
		node := obj(entry)
		graph[str(node, "id")], _ = stringsOf(node["depends"])
	}
	seen := map[string]bool{}
	var visit func(string)
	visit = func(id string) {
		for _, parent := range graph[id] {
			if !seen[parent] {
				seen[parent] = true
				visit(parent)
			}
		}
	}
	visit(nodeID)
	return seen
}

func (s *store) returnReview(id, target, note, expected string) error {
	if target == "" || strings.TrimSpace(note) == "" {
		return errors.New("请选择返工节点并填写意见")
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		active, err := one(tx, `SELECT steps.node,runs.snapshot FROM steps JOIN runs USING(requirement)
			WHERE requirement=? AND steps.status='review'`, id)
		if err != nil {
			return err
		}
		if active == nil || expected != "" && str(active, "node") != expected {
			return errors.New("待审核节点已变化，请刷新详情")
		}
		nodeID := str(active, "node")
		snapshot := obj(fromJSON(active["snapshot"]))
		if !upstream(snapshot, nodeID)[target] {
			return errors.New("只能退回当前审核节点的上游 Agent")
		}
		previous, err := one(tx, "SELECT status FROM steps WHERE requirement=? AND node=?", id, target)
		if err != nil {
			return err
		}
		eligible := false
		reset := []string{target}
		for _, entry := range array(snapshot["nodes"]) {
			node := obj(entry)
			key := str(node, "id")
			if key == target {
				eligible = str(node, "type") == "agent"
			}
			if upstream(snapshot, key)[target] {
				reset = append(reset, key)
			}
		}
		if !eligible || str(previous, "status") != "done" {
			return errors.New("返工节点必须是已完成的 Agent")
		}
		artifact := input{"decision": "returned", "note": note, "return_to": target}
		if err := recordAttempt(tx, id, nodeID, "returned", artifact); err != nil {
			return err
		}
		if err := tx.Exec("UPDATE steps SET status='pending',lease=NULL,artifact=NULL WHERE requirement=? AND node IN ?", id, reset).Error; err != nil {
			return err
		}
		return advance(tx, id, snapshot)
	})
}

func reviewFeedback(tx *gorm.DB, requirement, node string) (string, error) {
	row, err := one(tx, `SELECT artifact FROM step_attempts WHERE requirement=? AND status='returned'
		AND json_extract(artifact,'$.return_to')=? ORDER BY id DESC LIMIT 1`, requirement, node)
	return str(obj(fromJSON(row["artifact"])), "note"), err
}
