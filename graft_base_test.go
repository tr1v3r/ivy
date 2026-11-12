package ivy

import (
	"sync/atomic"
	"testing"

	"github.com/tr1v3r/ivy/driver"
)

func TestGraft_PreservesBaseUntilParentRead(t *testing.T) {
	for _, mode := range []string{"standard", "lazy", "instant", "ttl"} {
		for _, parentKind := range []string{"content", "nil"} {
			t.Run(mode+"_"+parentKind, func(t *testing.T) {
				source := graftTestTree(t, mode, "SOURCE", NewDirective("/", appendProc("-old")))
				graftTestGet(t, source, "/", "SOURCE-old")

				root := graftTestTree(t, "standard", "PARENT")
				wantBase := "PARENT"
				if parentKind == "nil" {
					wantBase = ""
					proc := &driver.RawProcessor{Proc: func(*driver.RealizeContext, []byte) ([]byte, error) {
						return nil, nil
					}}
					if err := root.Set(NewDirective("/", proc)); err != nil {
						t.Fatal(err)
					}
				}

				if err := root.Graft("/slot", source); err != nil {
					t.Fatal(err)
				}

				if got := string(source.getBase()); got != "SOURCE" {
					t.Fatalf("base before parent read = %q; want SOURCE", got)
				}

				if source.get() != nil || !source.basePending.Load() {
					t.Fatal("Graft retained old output or marked inherited base ready")
				}

				var calls atomic.Int32
				if err := source.Set(NewDirective("/", graftTestCountProc(&calls, "-new"))); err != nil {
					t.Fatal(err)
				}

				if calls.Load() != 0 {
					t.Fatal("Set evaluated against the retained old base before parent read")
				}

				graftTestGet(t, root, "/slot", wantBase+"-new")

				if got := source.getBase(); string(got) != wantBase || (parentKind == "nil" && got != nil) {
					t.Fatalf("base after parent read = %#v; want %q (nil parent: %v)", got, wantBase, parentKind == "nil")
				}

				if calls.Load() != 1 || source.basePending.Load() {
					t.Fatalf("calls = %d, pending = %v; want one completed realization", calls.Load(), source.basePending.Load())
				}
			})
		}
	}
}

func TestGraft_DetachRetainsLastBase(t *testing.T) {
	for _, mode := range []string{"standard", "lazy", "instant", "ttl"} {
		for _, readBeforeDetach := range []bool{false, true} {
			name := mode + "_before_parent_read"
			if readBeforeDetach {
				name = mode + "_after_parent_read"
			}

			t.Run(name, func(t *testing.T) {
				source := graftTestTree(t, mode, "SOURCE", NewDirective("/", appendProc("-s")))
				graftTestGet(t, source, "/", "SOURCE-s")
				root := graftTestTree(t, "standard", "PARENT")

				if err := root.Graft("/slot", source); err != nil {
					t.Fatal(err)
				}

				wantBase := "SOURCE"
				if readBeforeDetach {
					graftTestGet(t, root, "/slot", "PARENT-s")
					wantBase = "PARENT"
				}

				if err := root.Del("/slot"); err != nil {
					t.Fatal(err)
				}

				graftTestGet(t, source, "/", wantBase+"-s")

				if got := string(source.getBase()); got != wantBase {
					t.Fatalf("detached base = %q; want %q", got, wantBase)
				}
			})
		}
	}
}
