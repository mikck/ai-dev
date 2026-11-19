package main

import "testing"

func TestWorkflowRoundTrip(t *testing.T) {
	s, err := openStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := input{"name": "Example"}
	workspace, err := s.workspace(w)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := s.repository(input{"workspace": workspace, "provider": "github", "url": "https://github.com/example/api", "base": "main"})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := s.resource(input{"workspace": workspace, "kind": "agent", "name": "Codex", "config": input{"engine": "codex"}})
	if err != nil {
		t.Fatal(err)
	}
	sop, err := s.saveSOP(input{"workspace": workspace, "name": "Review", "nodes": []any{
		input{"id": "build", "type": "agent", "name": "Build", "agent": agent},
		input{"id": "review", "type": "review", "name": "Review", "depends": []any{"build"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.publishSOP(input{"workspace": workspace, "identifier": sop}); err != nil {
		t.Fatal(err)
	}
	id, err := s.requirement(input{"workspace": workspace, "title": "Change", "description": "Implement", "repositories": []any{repo}, "sop": sop})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.register("runner", "Runner", "proof"); err != nil {
		t.Fatal(err)
	}
	job, err := s.claim("runner")
	if err != nil || job == nil {
		t.Fatalf("job = %v, err = %v", job, err)
	}
	if err := s.complete("runner", str(job, "lease"), input{"output": "done"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.review(id, "approved", "OK", "review"); err != nil {
		t.Fatal(err)
	}
	item, err := s.item(id)
	if err != nil || str(item, "status") != "delivered" {
		t.Fatalf("item = %v, err = %v", item, err)
	}
}

func TestExistingDatabaseAndWorkspaceIsolation(t *testing.T) {
	root := t.TempDir()
	s, err := openStore(root)
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.workspace(input{"name": "A"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.workspace(input{"name": "B"})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := s.repository(input{"workspace": b, "provider": "gitlab", "url": "https://gitlab.com/example/web", "base": "main"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.requirement(input{"workspace": a, "title": "Wrong", "description": "Cross workspace", "repositories": []any{foreign}}); err == nil {
		t.Fatal("cross-workspace repository accepted")
	}
	agent, err := s.resource(input{"workspace": a, "kind": "agent", "name": "Builder", "config": input{"engine": "claude"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.saveSOP(input{"workspace": a, "name": "Cycle", "nodes": []any{
		input{"id": "one", "type": "agent", "name": "Build", "agent": agent, "depends": []any{"two"}},
		input{"id": "two", "type": "review", "name": "Review", "depends": []any{"one"}},
	}}); err == nil {
		t.Fatal("cyclic SOP accepted")
	}
	db, err := s.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := openStore(root)
	if err != nil {
		t.Fatal(err)
	}
	setup, err := reopened.setup(a)
	if err != nil {
		t.Fatal(err)
	}
	resources := setup["resources"].([]input)
	if len(resources) != 1 || str(resources[0], "id") != agent {
		t.Fatalf("workspace data not restored: %v", setup)
	}
}
