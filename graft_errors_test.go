package ivy

import (
	"errors"
	"strings"
	"testing"
)

func TestGraft_MissingTreesWrapErrNotExistsTree(t *testing.T) {
	var nilNative *tree
	var nilForeign *graftTestForeignTree

	for _, tc := range []struct {
		name   string
		path   string
		source Tree
	}{
		{"nil_source", "/target", nil},
		{"typed_nil_native", "/target", nilNative},
		{"typed_nil_foreign", "/target", nilForeign},
		{"missing_parent", "/missing/target", graftTestTree(t, "lazy", "SOURCE")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := graftTestTree(t, "lazy", "ROOT")

			err := root.Graft(tc.path, tc.source)

			if !errors.Is(err, ErrNotExistsTree) {
				t.Fatalf("Graft error = %v; want ErrNotExistsTree", err)
			}

			if errors.Unwrap(err) == nil {
				t.Fatal("Graft discarded source/parent context instead of wrapping the sentinel")
			}
		})
	}

	source := graftTestTree(t, "lazy", "SOURCE")

	if err := nilNative.Graft("/target", source); !errors.Is(err, ErrNotExistsTree) {
		t.Fatalf("nil receiver error = %v; want ErrNotExistsTree", err)
	}
}

func TestGraft_InvalidStructureIsNotMissingTree(t *testing.T) {
	root := graftTestTree(t, "lazy", "ROOT", NewDirective("/child"))
	source := graftTestTree(t, "lazy", "SOURCE")

	for _, path := range []string{"/", "/bad//name"} {
		err := root.Graft(path, source)

		if err == nil || errors.Is(err, ErrNotExistsTree) {
			t.Fatalf("Graft(%q) error = %v; want a non-missing-tree validation error", path, err)
		}
	}

	err := root.Graft("/child/cycle", root)

	if err == nil || errors.Is(err, ErrNotExistsTree) {
		t.Fatalf("cycle error = %v; want a structural validation error", err)
	}
}

func TestGraft_OwnershipCheckPrecedesCycleCheck(t *testing.T) {
	root := graftTestTree(t, "lazy", "ROOT", NewDirective("/branch/leaf"))
	branch := root.pickChild("branch").(*tree)
	leaf := branch.pickChild("leaf").(*tree)

	for _, tc := range []struct {
		name     string
		receiver *tree
		source   *tree
		want     string
	}{
		{"attached_self", branch, branch, "already attached"},
		{"attached_ancestor", leaf, branch, "already attached"},
		{"detached_self", root, root, "cycle"},
		{"detached_ancestor", leaf, root, "cycle"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.receiver.Graft("/slot", tc.source)

			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Graft error = %v; want %q rejection", err, tc.want)
			}
		})
	}
}

func TestCycleCheck(t *testing.T) {
	root := graftTestTree(t, "lazy", "ROOT", NewDirective("/child/leaf"))
	child := root.pickChild("child").(*tree)
	leaf := child.pickChild("leaf").(*tree)
	other := graftTestTree(t, "lazy", "OTHER")

	for _, tc := range []struct {
		name   string
		source *tree
		parent *tree
		want   bool
	}{
		{"self", root, root, true},
		{"direct_ancestor", root, child, true},
		{"deep_ancestor", root, leaf, true},
		{"descendant", leaf, root, false},
		{"unrelated", other, leaf, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := cycleCheck(tc.source, tc.parent); got != tc.want {
				t.Fatalf("cycleCheck = %v; want %v", got, tc.want)
			}
		})
	}
}
