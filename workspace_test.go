package main

import "testing"

func TestRepositoryLifecycleKeepsRequirementSnapshot(t *testing.T) {
	s, err := openStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.workspace(input{"name": "First"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.workspace(input{"name": "Second"})
	if err != nil {
		t.Fatal(err)
	}
	repo, err := s.repository(input{"workspace": first, "provider": "github", "url": "https://github.com/example/api", "base": "main"})
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.requirement(input{"workspace": first, "title": "Old", "description": "Retain repository", "repositories": []any{repo}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.deleteRepository(input{"workspace": second, "identifier": repo}); err == nil {
		t.Fatal("cross-workspace deletion accepted")
	}
	if _, err := s.repository(input{"workspace": first, "identifier": repo, "provider": "gitlab", "url": "https://gitlab.com/example/api", "base": "stable"}); err != nil {
		t.Fatal(err)
	}
	if err := s.deleteRepository(input{"workspace": first, "identifier": repo}); err != nil {
		t.Fatal(err)
	}
	item, err := s.item(task)
	if err != nil {
		t.Fatal(err)
	}
	targets := item["targets"].([]input)
	if len(targets) != 1 || str(targets[0], "base") != "main" || str(targets[0], "provider") != "github" {
		t.Fatalf("historical repository changed: %v", targets)
	}
	if _, err := s.requirement(input{"workspace": first, "title": "New", "description": "Use archived repository", "repositories": []any{repo}}); err == nil {
		t.Fatal("archived repository accepted")
	}
}

func TestDeletePublishedSOPKeepsHistory(t *testing.T) {
	s, err := openStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := s.workspace(input{"name": "Workspace"})
	if err != nil {
		t.Fatal(err)
	}
	repo, err := s.repository(input{"workspace": workspace, "provider": "github", "url": "https://github.com/example/api", "base": "main"})
	if err != nil {
		t.Fatal(err)
	}
	sopPayload := input{"workspace": workspace, "name": "Review",
		"nodes": []any{input{"id": "review", "name": "Review", "type": "review"}}}
	sop, err := s.saveSOP(sopPayload)
	if err != nil {
		t.Fatal(err)
	}
	sopKey := input{"workspace": workspace, "identifier": sop}
	if _, err := s.publishSOP(sopKey); err != nil {
		t.Fatal(err)
	}
	taskPayload := input{"workspace": workspace, "title": "Existing", "description": "Preserve", "repositories": []any{repo}, "sop": sop}
	task, err := s.requirement(taskPayload)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.deleteSOP(sopKey); err != nil {
		t.Fatal(err)
	}
	if _, err := s.requirement(taskPayload); err == nil {
		t.Fatal("archived SOP accepted")
	}
	setup, err := s.setup(workspace)
	if err != nil || len(setup["sops"].([]input)) != 0 {
		t.Fatalf("archived SOP still listed: %v %v", setup, err)
	}
	if _, err := s.saveSOP(sopPayload); err != nil {
		t.Fatal("cannot recreate name:", err)
	}
	item, err := s.item(task)
	if err != nil || str(item, "sop") != "Review" {
		t.Fatalf("historical workflow lost: %v %v", item, err)
	}
	if err := s.review(task, "approved", "OK", "review"); err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowPositionPersistsAndRejectsInvalidCoordinates(t *testing.T) {
	s, err := openStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := s.workspace(input{"name": "Canvas"})
	if err != nil {
		t.Fatal(err)
	}
	node := input{"id": "review", "name": "Review", "type": "review", "position": input{"x": float64(160), "y": float64(-42)}}
	payload := input{"workspace": workspace, "name": "Canvas SOP", "nodes": []any{node}}
	if _, err := s.saveSOP(payload); err != nil {
		t.Fatal(err)
	}
	setup, err := s.setup(workspace)
	if err != nil {
		t.Fatal(err)
	}
	sop := setup["sops"].([]input)[0]
	stored := obj(obj(array(sop["nodes"])[0])["position"])
	if stored["x"] != float64(160) || stored["y"] != float64(-42) {
		t.Fatalf("unexpected position: %v", stored)
	}
	id := str(sop, "id")
	if _, err := s.publishSOP(input{"workspace": workspace, "identifier": id}); err != nil {
		t.Fatal(err)
	}
	version, err := one(s.db, "SELECT snapshot FROM sop_versions WHERE sop=?", id)
	if err != nil {
		t.Fatal(err)
	}
	published := obj(fromJSON(version["snapshot"]))
	persisted := obj(obj(array(published["nodes"])[0])["position"])
	if persisted["x"] != stored["x"] || persisted["y"] != stored["y"] {
		t.Fatalf("published coordinates differ: %v", persisted)
	}
	node["position"] = input{"x": "bad", "y": float64(0)}
	payload["name"] = "Invalid canvas"
	if _, err := s.saveSOP(payload); err == nil {
		t.Fatal("invalid position accepted")
	}
}
