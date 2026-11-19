package main

import "gorm.io/gorm"

func migrateWorkspaceSettings(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		result := tx.Exec("INSERT OR IGNORE INTO migrations(name) VALUES ('workspace-settings-v1')")
		if result.Error != nil || result.RowsAffected == 0 {
			return result.Error
		}
		return tx.Exec(`INSERT INTO target_settings(requirement,repository,provider,url,base,verify)
			SELECT targets.requirement,repositories.id,provider,url,base,verify FROM targets
			JOIN repositories ON repositories.id=targets.repository`).Error
	})
}

func (s *store) deleteRepository(data input) error {
	return changed(s.db.Exec(`INSERT OR IGNORE INTO archived_repositories(id)
		SELECT id FROM repositories WHERE workspace=? AND id=?`, str(data, "workspace"), str(data, "identifier")), "当前 Workspace 中没有此仓库")
}

func (s *store) saveRepository(id, workspace string, config input) (string, error) {
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if id != "" {
			return changed(tx.Table("repositories").Where(`id=? AND workspace=?
				AND NOT EXISTS (SELECT 1 FROM archived_repositories WHERE id=repositories.id)`, id, workspace).Updates(config),
				"当前 Workspace 中没有此仓库")
		}
		archived, err := one(tx, `SELECT repositories.id FROM repositories JOIN archived_repositories USING(id)
			WHERE workspace=? AND url=?`, workspace, config["url"])
		if err != nil {
			return err
		}
		if archived != nil {
			id = str(archived, "id")
			if err := tx.Table("repositories").Where("id=?", id).Updates(config).Error; err != nil {
				return err
			}
			return tx.Exec("DELETE FROM archived_repositories WHERE id=?", id).Error
		}
		id = token(16)
		config["id"], config["workspace"] = id, workspace
		return tx.Table("repositories").Create(config).Error
	})
	return id, err
}

func taskRepositories(db *gorm.DB, id string) ([]input, error) {
	return rows(db, `SELECT target_settings.repository AS id,target_settings.provider,target_settings.url,
		target_settings.base,target_settings.verify,targets.review,requirements.workspace
		FROM target_settings JOIN targets USING(requirement,repository)
		JOIN requirements ON requirements.id=targets.requirement
		WHERE targets.requirement=? ORDER BY targets.rowid`, id)
}

func (s *store) deleteSOP(data input) error {
	id, workspace := str(data, "identifier"), str(data, "workspace")
	return s.db.Transaction(func(tx *gorm.DB) error {
		result := tx.Exec(`DELETE FROM sops WHERE workspace=? AND id=?
			AND NOT EXISTS (SELECT 1 FROM sop_versions WHERE sop=sops.id)`, workspace, id)
		if result.Error != nil || result.RowsAffected > 0 {
			return result.Error
		}
		if err := changed(tx.Exec(`INSERT OR IGNORE INTO archived_sops(id,name) SELECT id,name FROM sops WHERE workspace=? AND id=?`,
			workspace, id), "当前 Workspace 中没有此 SOP"); err != nil {
			return err
		}
		return tx.Exec("UPDATE sops SET name=name || ' [archived ' || id || ']' WHERE id=?", id).Error
	})
}
