package main

import (
	"crypto/subtle"
	"errors"
	"time"

	"gorm.io/gorm"
)

func now() float64             { return float64(time.Now().UnixMilli()) / 1000 }
func matches(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

func (s *store) register(id, name, proof string) error {
	if !keyPattern.MatchString(id) || name == "" || proof == "" {
		return errors.New("执行器标识或名称无效")
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		entry, err := one(tx, "SELECT proof FROM executors WHERE id = ?", id)
		if err != nil {
			return err
		}
		if entry != nil && !matches(str(entry, "proof"), proof) {
			return errors.New("执行器身份验证失败")
		}
		return tx.Exec(`INSERT INTO executors(id,name,proof,seen) VALUES (?,?,?,?)
			ON CONFLICT(id) DO UPDATE SET name=excluded.name,seen=excluded.seen`, id, name, proof, now()).Error
	})
}

func (s *store) verifies(id, proof string) bool {
	entry, err := one(s.db, "SELECT proof FROM executors WHERE id = ?", id)
	return err == nil && entry != nil && matches(str(entry, "proof"), proof)
}

func (s *store) heartbeat(id string) error {
	return s.db.Exec("UPDATE executors SET seen = ? WHERE id = ?", now(), id).Error
}

type executorStatus struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Seen    float64 `json:"seen"`
	Enabled bool    `json:"enabled"`
	Online  bool    `json:"online"`
	Running int     `json:"running"`
}

func (s *store) executors() ([]executorStatus, error) {
	entries := []executorStatus{}
	err := s.db.Raw(`SELECT id,name,seen,enabled,
		seen >= ? AS online,
		(SELECT count(*) FROM steps JOIN runs USING(requirement) WHERE runs.executor = executors.id AND steps.status='running') +
		(SELECT count(*) FROM drafts WHERE executor=executors.id AND status='running') +
		(SELECT count(*) FROM legacy_jobs JOIN requirements ON requirements.id=legacy_jobs.requirement
			WHERE executor=executors.id AND requirements.status IN ('planning','executing')) AS running
		FROM executors ORDER BY name`, now()-45).Scan(&entries).Error
	return entries, err
}

func (s *store) manage(id string, enabled bool) error {
	return changed(s.db.Exec("UPDATE executors SET enabled = ? WHERE id = ?", enabled, id), "执行器不存在")
}

func (s *store) recoverExecutor(id string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		row, err := one(tx, "SELECT id FROM executors WHERE id = ? AND enabled = 0 AND seen < ?", id, now()-45)
		if err != nil {
			return err
		}
		if row == nil {
			return errors.New("恢复前需停用执行器并确认已停止至少 45 秒")
		}
		if err := tx.Exec(`INSERT INTO step_attempts(requirement,node,status,artifact,finished)
            SELECT steps.requirement,steps.node,'failed',?,? FROM steps JOIN runs USING(requirement)
            WHERE runs.executor=? AND steps.status='running'`, toJSON(input{"error": "执行器失联，节点可以重试"}), now(), id).Error; err != nil {
			return err
		}
		queries := []struct {
			sql  string
			args []any
		}{
			{`UPDATE requirements SET status='sop_failed' WHERE id IN (SELECT requirement FROM steps JOIN runs USING(requirement)
				WHERE runs.executor = ? AND steps.status='running')`, []any{id}},
			{`UPDATE steps SET status='failed',artifact=? WHERE status='running'
				AND requirement IN (SELECT requirement FROM runs WHERE executor = ?)`,
				[]any{toJSON(input{"error": "执行器失联，节点可以重试"}), id}},
			{"UPDATE drafts SET status='failed',error='执行器失联，请重新提交对话' WHERE status='running' AND executor=?", []any{id}},
			{`UPDATE requirements SET status=CASE status WHEN 'planning' THEN 'failed_plan' ELSE 'failed_execute' END,
				log='执行器失联，任务可以重试' WHERE status IN ('planning','executing')
				AND id IN (SELECT requirement FROM legacy_jobs WHERE executor = ?)`, []any{id}},
		}
		for _, q := range queries {
			if err := tx.Exec(q.sql, q.args...).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *store) claim(id string) (input, error) {
	lease := token(16)
	var job input
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("UPDATE executors SET seen = ? WHERE id = ?", now(), id).Error; err != nil {
			return err
		}
		executor, err := one(tx, "SELECT id FROM executors WHERE id = ? AND enabled = 1", id)
		if err != nil || executor == nil {
			return err
		}
		step, err := one(tx, `SELECT steps.requirement,steps.node,runs.snapshot FROM steps JOIN runs USING(requirement)
			WHERE steps.status='ready' AND (runs.executor IS NULL OR runs.executor = ?)
			ORDER BY runs.rowid LIMIT 1`, id)
		if err != nil {
			return err
		}
		if step != nil {
			requirement, nodeID := str(step, "requirement"), str(step, "node")
			if err := tx.Exec("UPDATE runs SET executor=? WHERE requirement=?", id, requirement).Error; err != nil {
				return err
			}
			if err := tx.Exec("UPDATE steps SET status='running',lease=? WHERE requirement=? AND node=?", lease, requirement, nodeID).Error; err != nil {
				return err
			}
			if err := tx.Exec("UPDATE requirements SET status='sop_running' WHERE id=?", requirement).Error; err != nil {
				return err
			}
			item, err := itemAt(tx, requirement, false)
			if err != nil {
				return err
			}
			for _, value := range array(obj(fromJSON(step["snapshot"]))["nodes"]) {
				node := obj(value)
				if str(node, "id") == nodeID {
					feedback, err := reviewFeedback(tx, requirement, nodeID)
					job = input{"kind": "node", "lease": lease, "item": item, "node": node, "feedback": feedback}
					return err
				}
			}
			return errors.New("找不到节点快照")
		}
		draft, err := one(tx, "SELECT * FROM drafts WHERE status='queued' ORDER BY rowid LIMIT 1")
		if err != nil {
			return err
		}
		if draft != nil {
			if err := tx.Exec("UPDATE drafts SET status='running',executor=?,lease=? WHERE id=?", id, lease, draft["id"]).Error; err != nil {
				return err
			}
			agents, err := rows(tx, "SELECT id,name FROM resources WHERE workspace=? AND kind='agent' ORDER BY name", draft["workspace"])
			if err != nil {
				return err
			}
			job = input{"kind": "draft", "id": draft["id"], "lease": lease, "engine": draft["engine"], "messages": fromJSON(draft["messages"]), "agents": agents}
			return nil
		}
		legacy, err := one(tx, `SELECT requirements.id,requirements.status FROM requirements
			LEFT JOIN legacy_jobs ON legacy_jobs.requirement = requirements.id
			WHERE requirements.status IN ('queued_plan','queued_execute') AND (legacy_jobs.executor IS NULL OR legacy_jobs.executor = ?)
			ORDER BY requirements.rowid LIMIT 1`, id)
		if err != nil || legacy == nil {
			return err
		}
		requirement := str(legacy, "id")
		kind, status := "legacy_execute", "executing"
		if str(legacy, "status") == "queued_plan" {
			kind, status = "legacy_plan", "planning"
		}
		if err := tx.Exec(`INSERT INTO legacy_jobs(requirement,executor,lease) VALUES (?,?,?)
			ON CONFLICT(requirement) DO UPDATE SET lease=excluded.lease`, requirement, id, lease).Error; err != nil {
			return err
		}
		if err := tx.Exec("UPDATE requirements SET status=? WHERE id=?", status, requirement).Error; err != nil {
			return err
		}
		item, err := itemAt(tx, requirement, false)
		if err != nil {
			return err
		}
		job = input{"kind": kind, "lease": lease, "item": item}
		return nil
	})
	return job, err
}

func (s *store) draft(data input) (string, error) {
	engine := str(data, "engine")
	messages := array(data["messages"])
	if !engineOK(engine) || len(messages) == 0 || len(messages) > 20 || str(obj(messages[len(messages)-1]), "role") != "user" {
		return "", errors.New("AI 对话格式无效")
	}
	for _, value := range messages {
		msg := obj(value)
		role := str(msg, "role")
		if _, ok := msg["content"].(string); !ok || (role != "user" && role != "assistant") {
			return "", errors.New("AI 对话格式无效")
		}
	}
	id := token(16)
	return id, s.db.Table("drafts").Create(input{"id": id, "workspace": str(data, "workspace"), "engine": engine,
		"status": "queued", "messages": toJSON(messages)}).Error
}

func (s *store) getDraft(id string) (input, error) {
	entry, err := one(s.db, "SELECT id,status,nodes,error FROM drafts WHERE id = ?", id)
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, errors.New("对话草稿不存在")
	}
	entry["nodes"] = fromJSON(entry["nodes"])
	return entry, nil
}

func updateReviews(tx *gorm.DB, id string, artifact input) (string, error) {
	reviews := obj(artifact["reviews"])
	if len(reviews) == 0 {
		return "", nil
	}
	repositories, err := taskRepositories(tx, id)
	if err != nil {
		return "", err
	}
	problem := ""
	for _, repo := range repositories {
		key := str(repo, "id")
		if review, ok := reviews[key]; ok {
			link, _ := review.(string)
			if !validReview(repo, link) {
				problem = "发布工具未返回有效的审核链接"
				continue
			}
			if err := tx.Exec("UPDATE targets SET review=? WHERE requirement=? AND repository=?", link, id, key).Error; err != nil {
				return "", err
			}
		}
	}
	return problem, nil
}

func (s *store) complete(id, lease string, artifact input, failure string) error {
	if artifact == nil {
		return errors.New("节点产物格式无效")
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		step, err := one(tx, `SELECT steps.*,runs.snapshot FROM steps JOIN runs USING(requirement)
			WHERE lease = ? AND executor = ?`, lease, id)
		if err != nil {
			return err
		}
		if step != nil {
			if str(step, "status") != "running" {
				return nil
			}
			requirement := str(step, "requirement")
			problem, err := updateReviews(tx, requirement, artifact)
			if err != nil {
				return err
			}
			if failure == "" {
				failure = problem
			}
			status := "done"
			if failure != "" {
				artifact["error"] = failure
				status = "failed"
			}
			if err := recordAttempt(tx, requirement, str(step, "node"), status, artifact); err != nil {
				return err
			}
			if err := tx.Exec("UPDATE steps SET status=?,artifact=? WHERE lease=?", status, toJSON(artifact), lease).Error; err != nil {
				return err
			}
			if failure != "" {
				return tx.Exec("UPDATE requirements SET status='sop_failed' WHERE id=?", requirement).Error
			}
			return advance(tx, requirement, obj(fromJSON(step["snapshot"])))
		}
		draft, err := one(tx, "SELECT * FROM drafts WHERE lease=? AND executor=?", lease, id)
		if err != nil {
			return err
		}
		if draft != nil {
			if str(draft, "status") != "running" {
				return nil
			}
			var nodes []input
			if failure == "" {
				nodes, err = validateNodes(tx, str(draft, "workspace"), artifact["nodes"])
				if err != nil {
					failure = err.Error()
				}
			}
			status := "done"
			if failure != "" {
				status = "failed"
			}
			return tx.Exec("UPDATE drafts SET status=?,nodes=?,error=? WHERE id=?", status, toJSON(nodes), failure, draft["id"]).Error
		}
		legacy, err := one(tx, `SELECT requirements.id,requirements.status FROM legacy_jobs
			JOIN requirements ON requirements.id=legacy_jobs.requirement WHERE lease=? AND executor=?`, lease, id)
		if err != nil {
			return err
		}
		// Reports may be retransmitted after a successful write or an administrator recovery.
		if legacy == nil {
			return nil
		}
		requirement, status := str(legacy, "id"), str(legacy, "status")
		output := str(artifact, "output")
		if status == "planning" {
			if failure != "" || output == "" {
				if failure == "" {
					failure = "AI 未返回计划"
				}
				return tx.Exec("UPDATE requirements SET status='failed_plan',log=? WHERE id=?", failure, requirement).Error
			}
			return tx.Exec("UPDATE requirements SET status='awaiting_approval',plan=?,log=NULL WHERE id=?", output, requirement).Error
		}
		if status == "executing" {
			problem, err := updateReviews(tx, requirement, artifact)
			if err != nil {
				return err
			}
			if failure == "" {
				failure = problem
			}
			status = "delivered"
			if failure != "" {
				status, output = "failed_execute", failure
			}
			return tx.Exec("UPDATE requirements SET status=?,log=? WHERE id=?", status, output, requirement).Error
		}
		return nil
	})
}
