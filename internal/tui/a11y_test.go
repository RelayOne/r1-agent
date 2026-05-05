package tui

import (
	"reflect"
	"testing"
)

// laneSidebarFixture mirrors the spec §7 Storybook example. It is the
// canonical multi-level a11y tree for unit tests.
func laneSidebarFixture() A11yNode {
	return A11yNode{
		StableID: "lane-sidebar",
		Role:     "complementary",
		Name:     "Agents sidebar",
		Children: []A11yNode{
			{
				StableID: "lane-memory-curator",
				Role:     "listitem",
				Name:     "Lane memory-curator",
				Children: []A11yNode{
					{
						StableID: "lane-memory-curator-kill-button",
						Role:     "button",
						Name:     "Kill lane memory-curator",
						State:    map[string]string{"pressed": "false"},
					},
					{
						StableID: "lane-memory-curator-pin-button",
						Role:     "button",
						Name:     "Pin lane memory-curator",
					},
				},
			},
			{
				StableID: "lane-rule-check",
				Role:     "listitem",
				Name:     "Lane rule-check",
				Children: []A11yNode{
					{
						StableID: "lane-rule-check-kill-button",
						Role:     "button",
						Name:     "Kill lane rule-check",
					},
				},
			},
		},
	}
}

func TestFlattenA11y_DepthFirstOrder(t *testing.T) {
	root := laneSidebarFixture()
	got := FlattenA11y(root)
	wantIDs := []string{
		"lane-sidebar",
		"lane-memory-curator",
		"lane-memory-curator-kill-button",
		"lane-memory-curator-pin-button",
		"lane-rule-check",
		"lane-rule-check-kill-button",
	}
	if len(got) != len(wantIDs) {
		t.Fatalf("got %d nodes, want %d", len(got), len(wantIDs))
	}
	for i, n := range got {
		if n.StableID != wantIDs[i] {
			t.Errorf("got[%d].StableID = %q, want %q", i, n.StableID, wantIDs[i])
		}
	}
}

func TestFindByStableID_HitAtRoot(t *testing.T) {
	root := laneSidebarFixture()
	got, ok := FindByStableID(root, "lane-sidebar")
	if !ok {
		t.Fatal("root id should be findable")
	}
	if got.Role != "complementary" {
		t.Errorf("got role %q, want complementary", got.Role)
	}
}

func TestFindByStableID_HitDeep(t *testing.T) {
	root := laneSidebarFixture()
	got, ok := FindByStableID(root, "lane-memory-curator-kill-button")
	if !ok {
		t.Fatal("deep id should be findable")
	}
	if got.Role != "button" {
		t.Errorf("got role %q, want button", got.Role)
	}
}

func TestFindByStableID_Miss(t *testing.T) {
	root := laneSidebarFixture()
	_, ok := FindByStableID(root, "nonexistent")
	if ok {
		t.Error("missing id should return ok=false")
	}
}

func TestFindByRoleAndName_FirstMatch(t *testing.T) {
	root := laneSidebarFixture()
	got, ok := FindByRoleAndName(root, "button", "Kill lane memory-curator")
	if !ok {
		t.Fatal("button by name should be findable")
	}
	if got.StableID != "lane-memory-curator-kill-button" {
		t.Errorf("got %q, want lane-memory-curator-kill-button", got.StableID)
	}
}

func TestFindByRoleAndName_SubstringMatch(t *testing.T) {
	root := laneSidebarFixture()
	// Substring "Kill" matches all kill buttons; first hit should be
	// the memory-curator one (DFS order).
	got, ok := FindByRoleAndName(root, "button", "Kill")
	if !ok {
		t.Fatal("substring match expected")
	}
	if got.StableID != "lane-memory-curator-kill-button" {
		t.Errorf("got %q, want lane-memory-curator-kill-button (DFS first)",
			got.StableID)
	}
}

func TestFindByRoleAndName_RoleFilter(t *testing.T) {
	root := laneSidebarFixture()
	// Searching by role=listitem with name "memory-curator" must NOT
	// return the button (which contains the same substring).
	got, ok := FindByRoleAndName(root, "listitem", "memory-curator")
	if !ok {
		t.Fatal("listitem by name should be findable")
	}
	if got.Role != "listitem" {
		t.Errorf("got role %q, want listitem", got.Role)
	}
}

func TestFindByRoleAndName_Miss(t *testing.T) {
	root := laneSidebarFixture()
	_, ok := FindByRoleAndName(root, "button", "Nonexistent button")
	if ok {
		t.Error("missing role+name should return ok=false")
	}
}

// fakeModel exercises the A11yEmitter interface — it MUST satisfy the
// interface assignment compile-check.
type fakeModel struct {
	id   string
	name string
}

func (f fakeModel) StableID() string { return f.id }
func (f fakeModel) A11y() A11yNode {
	return A11yNode{StableID: f.id, Role: "list", Name: f.name}
}

func TestA11yEmitter_InterfaceContract(t *testing.T) {
	var _ A11yEmitter = fakeModel{}
	m := fakeModel{id: "m-1", name: "Demo list"}
	got := m.A11y()
	want := A11yNode{StableID: "m-1", Role: "list", Name: "Demo list"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("A11y() = %+v, want %+v", got, want)
	}
	if m.StableID() != "m-1" {
		t.Errorf("StableID() = %q, want m-1", m.StableID())
	}
}

func TestIndexOfSubstring_BasicCases(t *testing.T) {
	cases := []struct {
		hay, needle string
		want        int
	}{
		{"abcdef", "cd", 2},
		{"abcdef", "xyz", -1},
		{"abc", "", 0},
		{"abc", "abcd", -1},
		{"abc", "abc", 0},
	}
	for _, tc := range cases {
		if got := indexOfSubstring(tc.hay, tc.needle); got != tc.want {
			t.Errorf("indexOfSubstring(%q, %q) = %d, want %d",
				tc.hay, tc.needle, got, tc.want)
		}
	}
}
