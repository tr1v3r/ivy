package ivy

import (
	"context"
	"testing"

	"github.com/tr1v3r/ivy/driver"
)

func TestGraft_LocalQueryRealizesMountedAncestors(t *testing.T) {
	for _, withContext := range []bool{false, true} {
		t.Run(map[bool]string{false: "Get", true: "GetWithContext"}[withContext], func(t *testing.T) {
			root := graftTestTree(t, "lazy", "D", NewDirective("/", appendProc("-old")))
			source := graftTestTree(t, "standard", "SOURCE",
				NewDirective("/", appendProc("-s")), NewDirective("/leaf", appendProc("-l")))
			if err := root.Graft("/alias", source); err != nil {
				t.Fatal(err)
			}
			query := func(want string) {
				t.Helper()
				if !withContext {
					graftTestGet(t, source, "/leaf", want)
					return
				}
				rc := &driver.RealizeContext{Context: context.Background()}
				got, err := source.GetWithContext(rc, "/leaf")
				if err != nil || string(got) != want {
					t.Fatalf("local contextual query = %q, %v; want %q", got, err, want)
				}
				if rc.TreePath != "/alias/leaf" || string(rc.ParentContent) != want[:len(want)-2] {
					t.Fatalf("local query context = %+v", rc)
				}
			}
			// No outer-root query has initialized the inherited base yet.
			query("D-old-s-l")
			if err := root.Set(NewDirective("/", appendProc("-new"))); err != nil {
				t.Fatal(err)
			}
			// The retained source handle must observe refreshed ancestors too.
			query("D-new-s-l")
		})
	}
}

func TestGraft_RejectsAmbiguousNativeNames(t *testing.T) {
	dotDriver := &testPathDriver{Modem: driver.DummyModem, DelimiterPathParser: new(driver.DelimiterPathParser).WithDelimiter(".")}
	bad, err := NewLazyTree(dotDriver, "source", "SOURCE", NewDirective(".a/b"))
	if err != nil {
		t.Fatal(err)
	}
	root := graftTestTree(t, "standard", "D", NewDirective("/mount", appendProc("-old")))
	old := root.pickChild("mount")
	if err := root.Graft("/mount", bad); err == nil {
		t.Fatal("accepted slash-containing source alias under a slash root")
	}
	if root.pickChild("mount") != old || bad.Path() != "" {
		t.Fatal("failed alias validation changed ownership or replaced old node")
	}
	graftTestGet(t, root, "/mount", "D-old")

	good, err := NewLazyTree[Directive](dotDriver, "source", "SOURCE")
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Graft("/mount", good); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		receiver Tree
		path     string
	}{
		{good, ".a/b"},       // unrepresentable through outer slash root
		{root, "/mount/a.b"}, // unrepresentable through retained dot source
	} {
		if err := tc.receiver.Set(NewDirective(tc.path)); err == nil {
			t.Errorf("Set accepted an ambiguous name at %q", tc.path)
		}
		other := graftTestTree(t, "lazy", "OTHER")
		if err := tc.receiver.Graft(tc.path, other); err == nil {
			t.Errorf("Graft accepted an ambiguous name at %q", tc.path)
		}
		if other.Path() != "" {
			t.Fatal("rejected source gained an owner")
		}
	}
}

func TestGraft_AttachedCallbackRejectsDuplicateOwnership(t *testing.T) {
	root := graftTestTree(t, "standard", "D")
	var source *tree
	var graftErr error
	source = graftTestTree(t, "lazy", "SOURCE", NewDirective("/", &driver.RawProcessor{
		Proc: func(_ *driver.RealizeContext, before []byte) ([]byte, error) {
			graftErr = root.Graft("/another", source)
			return before, nil
		},
	}))
	if err := root.Graft("/alias", source); err != nil {
		t.Fatal(err)
	}
	graftTestGet(t, root, "/alias", "D")
	if graftErr == nil || root.Has("/another") {
		t.Fatal("callback could attach an already owned source")
	}
}
