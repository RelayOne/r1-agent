package main

import (
	"reflect"
	"sort"
	"testing"
)

func TestToSet(t *testing.T) {
	in := []string{"a", "b", "c", "a"}
	set := toSet(in)
	if len(set) != 3 {
		t.Errorf("set size = %d, want 3", len(set))
	}
	if !set["a"] || !set["b"] || !set["c"] {
		t.Errorf("set missing items: %v", set)
	}
	if set["missing"] {
		t.Error("set should not contain unseen key")
	}
}

func TestToSet_Empty(t *testing.T) {
	set := toSet(nil)
	if len(set) != 0 {
		t.Errorf("nil input should produce empty set, got %d", len(set))
	}
}

func TestDiffFileSets_NewFilesOnly(t *testing.T) {
	pre := map[string]bool{"a.go": true, "b.go": true}
	post := []string{"a.go", "b.go", "c.go", "d.go"}
	diff := diffFileSets(post, pre)
	sort.Strings(diff)
	want := []string{"c.go", "d.go"}
	if !reflect.DeepEqual(diff, want) {
		t.Errorf("diff = %v, want %v", diff, want)
	}
}

func TestDiffFileSets_NoNewFiles(t *testing.T) {
	pre := map[string]bool{"a.go": true, "b.go": true}
	post := []string{"a.go", "b.go"}
	diff := diffFileSets(post, pre)
	if len(diff) != 0 {
		t.Errorf("expected no new files, got %v", diff)
	}
}

func TestDiffFileSets_AllNew(t *testing.T) {
	pre := map[string]bool{}
	post := []string{"a.go", "b.go"}
	diff := diffFileSets(post, pre)
	if len(diff) != 2 {
		t.Errorf("expected 2 new files, got %v", diff)
	}
}

func TestDiffFileSets_EmptyPost(t *testing.T) {
	pre := map[string]bool{"a.go": true}
	post := []string{}
	diff := diffFileSets(post, pre)
	if len(diff) != 0 {
		t.Errorf("empty post should produce empty diff, got %v", diff)
	}
}
