package rbac

import (
	"errors"
	"sync"
	"testing"
)

func TestPredefinedRoles(t *testing.T) {
	// Admin should have all permissions
	admin := PredefinedRoles["admin"]
	for _, perm := range []Permission{PermPlanCreate, PermBuildExecute, PermMergeForce, PermPoolManage, PermShipExecute} {
		if !admin.Has(perm) {
			t.Errorf("admin should have %s", perm)
		}
	}

	// Developer should NOT have force merge or pool management
	dev := PredefinedRoles["developer"]
	if dev.Has(PermMergeForce) {
		t.Error("developer should not have merge:force")
	}
	if dev.Has(PermPoolManage) {
		t.Error("developer should not have pool:manage")
	}
	if !dev.Has(PermBuildExecute) {
		t.Error("developer should have build:execute")
	}

	// Viewer should only have read permissions
	viewer := PredefinedRoles["viewer"]
	if viewer.Has(PermBuildExecute) {
		t.Error("viewer should not have build:execute")
	}
	if !viewer.Has(PermPlanRead) {
		t.Error("viewer should have plan:read")
	}

	// CI should have build and merge but no force merge
	ci := PredefinedRoles["ci"]
	if !ci.Has(PermBuildExecute) {
		t.Error("ci should have build:execute")
	}
	if ci.Has(PermMergeForce) {
		t.Error("ci should not have merge:force")
	}
}

func TestPolicy_DefaultRole(t *testing.T) {
	p := NewPolicy("admin")
	role := p.RoleFor("unknown-user")
	if role.Name != "admin" {
		t.Errorf("default role should be admin, got %s", role.Name)
	}
}

func TestPolicy_Assign(t *testing.T) {
	p := NewPolicy("viewer")
	if err := p.Assign("alice", "admin"); err != nil {
		t.Fatalf("assign: %v", err)
	}
	if err := p.Assign("bob", "developer"); err != nil {
		t.Fatalf("assign: %v", err)
	}

	if p.RoleFor("alice").Name != "admin" {
		t.Error("alice should be admin")
	}
	if p.RoleFor("bob").Name != "developer" {
		t.Error("bob should be developer")
	}
	if p.RoleFor("charlie").Name != "viewer" {
		t.Error("charlie should get default role viewer")
	}
}

func TestPolicy_Assign_InvalidRole(t *testing.T) {
	p := NewPolicy("admin")
	if err := p.Assign("alice", "superuser"); err == nil {
		t.Error("assigning non-existent role should fail")
	}
}

func TestPolicy_Check(t *testing.T) {
	p := NewPolicy("viewer")
	p.Assign("admin-user", "admin")
	p.Assign("dev-user", "developer")

	// Admin can do everything
	if err := p.Check("admin-user", PermMergeForce); err != nil {
		t.Errorf("admin should have merge:force: %v", err)
	}

	// Developer can build but not force merge
	if err := p.Check("dev-user", PermBuildExecute); err != nil {
		t.Errorf("developer should have build:execute: %v", err)
	}
	if err := p.Check("dev-user", PermMergeForce); err == nil {
		t.Error("developer should NOT have merge:force")
	}

	// Viewer can only view
	if err := p.Check("viewer-user", PermBuildExecute); err == nil {
		t.Error("viewer should NOT have build:execute")
	}
}

func TestPolicy_Require(t *testing.T) {
	p := NewPolicy("admin")

	// Admin has everything
	if err := p.Require("admin-user", PermBuildExecute, PermMergeApprove); err != nil {
		t.Errorf("admin should pass require: %v", err)
	}

	// Viewer missing permissions
	p.Assign("viewer", "viewer")
	if err := p.Require("viewer", PermBuildExecute, PermMergeApprove); err == nil {
		t.Error("viewer should fail require for build+merge")
	}
}

func TestPolicy_AddRole(t *testing.T) {
	p := NewPolicy("admin")
	p.AddRole(Role{
		Name:        "auditor",
		Description: "Audit-only access",
		Permissions: []Permission{PermAuditRun, PermAuditView, PermConfigView},
	})
	p.Assign("auditor-user", "auditor")

	if err := p.Check("auditor-user", PermAuditRun); err != nil {
		t.Errorf("auditor should have audit:run: %v", err)
	}
	if err := p.Check("auditor-user", PermBuildExecute); err == nil {
		t.Error("auditor should not have build:execute")
	}
}

func TestPolicy_ListRoles(t *testing.T) {
	p := NewPolicy("admin")
	roles := p.ListRoles()
	if len(roles) < 4 {
		t.Errorf("expected at least 4 predefined roles, got %d", len(roles))
	}
}

func TestPolicy_ListAssignments(t *testing.T) {
	p := NewPolicy("admin")
	p.Assign("alice", "admin")
	p.Assign("bob", "developer")

	assignments := p.ListAssignments()
	if len(assignments) != 2 {
		t.Errorf("expected 2 assignments, got %d", len(assignments))
	}
	if assignments["alice"] != "admin" {
		t.Error("alice should be admin")
	}
}

func TestAccessDeniedError(t *testing.T) {
	err := &AccessDeniedError{
		Identity:   "bob",
		Role:       "viewer",
		Permission: PermBuildExecute,
	}
	msg := err.Error()
	if msg == "" {
		t.Error("error message should not be empty")
	}

	// Should be unwrappable
	var ade *AccessDeniedError
	if !errors.As(err, &ade) {
		t.Error("should be assertable as AccessDeniedError")
	}
}

func TestPolicy_Concurrent(t *testing.T) {
	p := NewPolicy("admin")
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(3)
		go func(n int) {
			defer wg.Done()
			p.Check("user", PermBuildExecute)
		}(i)
		go func(n int) {
			defer wg.Done()
			p.RoleFor("user")
		}(i)
		go func(n int) {
			defer wg.Done()
			p.Snapshot()
		}(i)
	}
	wg.Wait()
}

// Snapshot is not in the main package, we test ListAssignments as the read path
func (p *Policy) Snapshot() map[string]string {
	return p.ListAssignments()
}

func TestRole_Has(t *testing.T) {
	r := Role{
		Name:        "test",
		Permissions: []Permission{PermPlanRead, PermConfigView},
	}
	if !r.Has(PermPlanRead) {
		t.Error("should have plan:read")
	}
	if r.Has(PermBuildExecute) {
		t.Error("should not have build:execute")
	}
}
