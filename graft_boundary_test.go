package ivy

import (
	"context"
	"errors"
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

func TestGraft_StaleCarryCannotPublishAfterRemount(t *testing.T) {
	for _, contextual := range []bool{false, true} {
		t.Run(map[bool]string{false: "Get", true: "GetWithContext"}[contextual], func(t *testing.T) {
			a := graftTestTree(t, "standard", "A")

			for i := 0; i < 10; i++ {
				if err := a.Set(NewDirective("/", appendProc("-old"))); err != nil {
					t.Fatal(err)
				}
			}

			source := graftTestTree(t, "lazy", "SOURCE",
				NewDirective("/", appendProc("-s")), NewDirective("/leaf", appendProc("-l")))

			if err := a.Graft("/slot", source); err != nil {
				t.Fatal(err)
			}

			// Model a query paused after selecting the old child and snapshot.
			oldBinding := source.mount.Load()
			oldContent, oldVersion := a.getWithVersion()

			if err := a.Del("/slot"); err != nil {
				t.Fatal(err)
			}

			b := graftTestTree(t, "standard", "B")

			if err := b.Graft("/slot", source); err != nil {
				t.Fatal(err)
			}

			q := newPathQuery(source.driver, "/leaf")
			var err error
			if contextual {
				_, err = source.getContextWithParent(&driver.RealizeContext{Context: context.Background()}, oldContent, oldVersion, q, oldBinding)
			} else {
				_, err = source.getWithParent(oldContent, oldVersion, q, oldBinding)
			}

			if !errors.Is(err, errMountChanged) {
				t.Fatalf("stale mount carry error = %v", err)
			}

			graftTestGet(t, b, "/slot/leaf", "B-s-l")
			graftTestGet(t, b, "/slot/leaf", "B-s-l")
			if source.realizedFrom >= oldVersion {
				t.Fatal("old parent's higher version poisoned the new inherited cache")
			}
		})
	}
}

func TestGraft_ConcurrentSetAndTransferKeepNamesAddressable(t *testing.T) {
	for i := 0; i < 200; i++ {
		root := graftTestTree(t, "standard", "D")
		dotDriver := &testPathDriver{Modem: driver.DummyModem, DelimiterPathParser: new(driver.DelimiterPathParser).WithDelimiter(".")}
		source, err := NewLazyTree[Directive](dotDriver, "source", "SOURCE")
		if err != nil {
			t.Fatal(err)
		}

		start := make(chan struct{})
		setDone, graftDone := make(chan error, 1), make(chan error, 1)
		go func() {
			<-start
			setDone <- source.Set(NewDirective(".a/b"))
		}()

		go func() {
			<-start
			graftDone <- root.Graft("/slot", source)
		}()

		close(start)
		setErr, graftErr := <-setDone, <-graftDone
		if setErr == nil && graftErr == nil {
			t.Fatal("Set raced with ownership transfer to create an unreachable alias")
		}
	}
}
