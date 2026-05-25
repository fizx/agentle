package store

import (
	"context"
	"testing"
)

func TestToolPolicyCRUD(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()

	// Unset => not found, no error.
	if _, ok, err := s.GetToolPolicy(ctx, "api.x.com", "GET"); ok || err != nil {
		t.Fatalf("unset policy: ok=%v err=%v", ok, err)
	}

	if err := s.PutToolPolicy(ctx, ToolPolicy{Server: "api.x.com", Tool: "GET", IsWrite: false}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutToolPolicy(ctx, ToolPolicy{Server: "api.x.com", Tool: "POST", IsWrite: true, Source: PolicyAnnotation}); err != nil {
		t.Fatal(err)
	}

	tp, ok, err := s.GetToolPolicy(ctx, "api.x.com", "GET")
	if err != nil || !ok || tp.IsWrite || tp.Source != PolicyOperator {
		t.Fatalf("GET policy = %+v ok=%v err=%v", tp, ok, err)
	}

	// Upsert flips the value, one row.
	if err := s.PutToolPolicy(ctx, ToolPolicy{Server: "api.x.com", Tool: "GET", IsWrite: true}); err != nil {
		t.Fatal(err)
	}
	tp, _, _ = s.GetToolPolicy(ctx, "api.x.com", "GET")
	if !tp.IsWrite {
		t.Fatal("upsert did not flip is_write")
	}

	list, _ := s.ListToolPolicies(ctx)
	if len(list) != 2 {
		t.Fatalf("list = %d", len(list))
	}

	if err := s.DeleteToolPolicy(ctx, "api.x.com", "GET"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.GetToolPolicy(ctx, "api.x.com", "GET"); ok {
		t.Fatal("policy not deleted")
	}
}
