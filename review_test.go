package main

import "testing"

func TestReviewReturnPreservesAttemptsAndPinnedVersion(t *testing.T) {
	s, err := openStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := s.workspace(input{"name": "Example"})
	if err != nil {
		t.Fatal(err)
	}
	repo, err := s.repository(input{"workspace": workspace, "provider": "github", "url": "https://github.com/example/api", "base": "main"})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := s.resource(input{"workspace": workspace, "kind": "agent", "name": "Builder", "config": input{"engine": "codex"}})
	if err != nil {
		t.Fatal(err)
	}
	nodes := []any{input{"id": "build", "type": "agent", "name": "Build", "agent": agent},
		input{"id": "audit", "type": "review", "name": "Audit", "depends": []any{"build"}}}
	sop, err := s.saveSOP(input{"workspace": workspace, "name": "First", "nodes": nodes})
	if err != nil {
		t.Fatal(err)
	}
	args := input{"workspace": workspace, "title": "Change", "description": "Implement", "repositories": []any{repo}, "sop": sop}
	if _, err := s.requirement(args); err == nil {
		t.Fatal("unpublished SOP accepted")
	}
	if _, err = s.publishSOP(input{"workspace": workspace, "identifier": sop}); err != nil {
		t.Fatal(err)
	}
	id, err := s.requirement(args)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.saveSOP(input{"workspace": workspace, "identifier": sop, "name": "Second", "nodes": nodes}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.publishSOP(input{"workspace": workspace, "identifier": sop}); err != nil {
		t.Fatal(err)
	}
	if err = s.register("runner", "Runner", "proof"); err != nil {
		t.Fatal(err)
	}
	first, err := s.claim("runner")
	if err != nil || first == nil {
		t.Fatalf("claim: %v %v", first, err)
	}
	if err = s.complete("runner", str(first, "lease"), input{"output": "attempt one"}, ""); err != nil {
		t.Fatal(err)
	}
	inbox, err := s.reviewInbox(workspace)
	if err != nil || len(inbox) != 1 {
		t.Fatalf("inbox: %v %v", inbox, err)
	}
	if err = s.returnReview(id, "build", "Fix the test", "audit"); err != nil {
		t.Fatal(err)
	}
	second, err := s.claim("runner")
	if err != nil || second == nil {
		t.Fatalf("retry claim: %v %v", second, err)
	}
	if str(second, "feedback") != "Fix the test" || str(obj(second["item"]), "sop") != "First" {
		t.Fatalf("job: %v", second)
	}
	if err = s.complete("runner", str(second, "lease"), input{"output": "attempt two"}, ""); err != nil {
		t.Fatal(err)
	}
	item, err := s.item(id)
	if err != nil {
		t.Fatal(err)
	}
	steps := item["nodes"].([]input)
	if got := steps[0]["attempts"].([]input); len(got) != 2 {
		t.Fatalf("attempts: %v", got)
	}
}
