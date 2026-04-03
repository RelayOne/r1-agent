// Package rbac implements Role-Based Access Control for Stoke operations.
// It enforces permission boundaries on who can execute plans, access pools,
// modify configurations, and approve merges. Roles are defined in the policy
// file and enforced at operation boundaries.
package rbac

import (
	"fmt"
	"sync"
)

// Permission represents a specific action that can be authorized.
type Permission string

// Standard permissions for Stoke operations.
const (
	PermPlanCreate   Permission = "plan:create"   // Create execution plans.
	PermPlanRead     Permission = "plan:read"      // Read existing plans.
	PermBuildExecute Permission = "build:execute"  // Execute build tasks.
	PermBuildCancel  Permission = "build:cancel"   // Cancel running builds.
	PermMergeApprove Permission = "merge:approve"  // Approve merges to main.
	PermMergeForce   Permission = "merge:force"    // Force-merge bypassing gates.
	PermPoolManage   Permission = "pool:manage"    // Add/remove subscription pools.
	PermPoolView     Permission = "pool:view"      // View pool status and utilization.
	PermConfigEdit   Permission = "config:edit"    // Edit policy and configuration.
	PermConfigView   Permission = "config:view"    // View policy and configuration.
	PermAuditRun     Permission = "audit:run"      // Run security audits.
	PermAuditView    Permission = "audit:view"     // View audit results.
	PermSessionView  Permission = "session:view"   // View session state and history.
	PermSessionClear Permission = "session:clear"  // Clear session state.
	PermShipExecute  Permission = "ship:execute"   // Run the ship convergence loop.
	PermRepairRun    Permission = "repair:run"     // Run repair workflows.
)

// Role represents a named set of permissions.
type Role struct {
	Name        string       `json:"name" yaml:"name"`
	Description string       `json:"description" yaml:"description"`
	Permissions []Permission `json:"permissions" yaml:"permissions"`
}

// Has returns true if the role includes the given permission.
func (r Role) Has(perm Permission) bool {
	for _, p := range r.Permissions {
		if p == perm {
			return true
		}
	}
	return false
}

// PredefinedRoles contains the built-in role definitions.
var PredefinedRoles = map[string]Role{
	"admin": {
		Name:        "admin",
		Description: "Full access to all operations",
		Permissions: []Permission{
			PermPlanCreate, PermPlanRead, PermBuildExecute, PermBuildCancel,
			PermMergeApprove, PermMergeForce, PermPoolManage, PermPoolView,
			PermConfigEdit, PermConfigView, PermAuditRun, PermAuditView,
			PermSessionView, PermSessionClear, PermShipExecute, PermRepairRun,
		},
	},
	"developer": {
		Name:        "developer",
		Description: "Build and plan access, no force-merge or pool management",
		Permissions: []Permission{
			PermPlanCreate, PermPlanRead, PermBuildExecute, PermBuildCancel,
			PermMergeApprove, PermPoolView, PermConfigView,
			PermAuditView, PermSessionView, PermShipExecute, PermRepairRun,
		},
	},
	"viewer": {
		Name:        "viewer",
		Description: "Read-only access to plans, builds, and audit results",
		Permissions: []Permission{
			PermPlanRead, PermPoolView, PermConfigView,
			PermAuditView, PermSessionView,
		},
	},
	"ci": {
		Name:        "ci",
		Description: "CI/CD automation: build, test, merge",
		Permissions: []Permission{
			PermPlanCreate, PermPlanRead, PermBuildExecute,
			PermMergeApprove, PermConfigView, PermAuditRun, PermAuditView,
			PermSessionView, PermSessionClear, PermShipExecute,
		},
	},
}

// Policy maps identities (usernames, API keys, etc.) to roles.
type Policy struct {
	mu          sync.RWMutex
	roles       map[string]Role   // role name -> Role
	assignments map[string]string // identity -> role name
	defaultRole string            // role for unassigned identities
}

// NewPolicy creates a policy with predefined roles and a default role.
func NewPolicy(defaultRole string) *Policy {
	roles := make(map[string]Role, len(PredefinedRoles))
	for k, v := range PredefinedRoles {
		roles[k] = v
	}
	if defaultRole == "" {
		defaultRole = "admin" // single-user default: full access
	}
	return &Policy{
		roles:       roles,
		assignments: make(map[string]string),
		defaultRole: defaultRole,
	}
}

// Assign maps an identity to a role. Returns an error if the role doesn't exist.
func (p *Policy) Assign(identity, roleName string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.roles[roleName]; !ok {
		return fmt.Errorf("unknown role %q", roleName)
	}
	p.assignments[identity] = roleName
	return nil
}

// AddRole registers a custom role.
func (p *Policy) AddRole(role Role) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.roles[role.Name] = role
}

// RoleFor returns the role assigned to the given identity, falling back to defaultRole.
func (p *Policy) RoleFor(identity string) Role {
	p.mu.RLock()
	defer p.mu.RUnlock()
	roleName, ok := p.assignments[identity]
	if !ok {
		roleName = p.defaultRole
	}
	if role, ok := p.roles[roleName]; ok {
		return role
	}
	return p.roles[p.defaultRole]
}

// Check returns nil if the identity has the required permission, or an error describing the denial.
func (p *Policy) Check(identity string, perm Permission) error {
	role := p.RoleFor(identity)
	if role.Has(perm) {
		return nil
	}
	return &AccessDeniedError{Identity: identity, Role: role.Name, Permission: perm}
}

// Require is like Check but for multiple permissions. All must be present.
func (p *Policy) Require(identity string, perms ...Permission) error {
	for _, perm := range perms {
		if err := p.Check(identity, perm); err != nil {
			return err
		}
	}
	return nil
}

// ListRoles returns all registered role names.
func (p *Policy) ListRoles() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	names := make([]string, 0, len(p.roles))
	for name := range p.roles {
		names = append(names, name)
	}
	return names
}

// ListAssignments returns all identity-to-role mappings.
func (p *Policy) ListAssignments() map[string]string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make(map[string]string, len(p.assignments))
	for k, v := range p.assignments {
		out[k] = v
	}
	return out
}

// AccessDeniedError is returned when a permission check fails.
type AccessDeniedError struct {
	Identity   string
	Role       string
	Permission Permission
}

// Error implements the error interface.
func (e *AccessDeniedError) Error() string {
	return fmt.Sprintf("access denied: identity %q (role %q) lacks permission %q", e.Identity, e.Role, e.Permission)
}
