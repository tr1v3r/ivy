package ivy

import (
	"context"
	"errors"
	"fmt"
	"sync"
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

func TestGraft_ParentUpdateBeforeFirstMountedQuery(t *testing.T) {
	dst := graftTestTree(t, "standard", "D", NewDirective("/", appendProc("-old")))
	var runs atomic.Int32
	src := graftTestTree(t, "standard", "SOURCE",
		NewDirective("/", graftTestCountProc(&runs, "-s")),
		NewDirective("/leaf", appendProc("-l")))

	graftTestGet(t, src, "/leaf", "SOURCE-s-l")
	runs.Store(0)

	if err := dst.Graft("/mount", src); err != nil {
		t.Fatal(err)
	}

	if runs.Load() != 0 {
		t.Fatal("standard source ran during Graft")
	}

	if err := dst.Set(NewDirective("/", appendProc("-new"))); err != nil {
		t.Fatal(err)
	}

	graftTestGet(t, dst, "/mount/leaf", "D-new-s-l")

	if err := dst.Set(NewDirective("/mount", appendProc("-changed"))); err != nil {
		t.Fatal(err)
	}

	graftTestGet(t, dst, "/mount/leaf", "D-new-changed-l")
}

func TestGraft_NestedMountMovesToDifferentDepth(t *testing.T) {
	src := graftTestTree(t, "lazy", "SOURCE", NewDirective("/", appendProc("-lazy")))
	standard := graftTestTree(t, "standard", "IGNORED", NewDirective("/", appendProc("-standard")))

	if err := src.Graft("/branch", standard); err != nil {
		t.Fatal(err)
	}

	leaf := graftTestTree(t, "instant", "IGNORED", NewDirective("/", appendProc("-live")))

	if err := src.Graft("/branch/live", leaf); err != nil {
		t.Fatal(err)
	}

	a := graftTestTree(t, "standard", "A")

	if err := a.Graft("/mount", src); err != nil {
		t.Fatal(err)
	}

	graftTestGet(t, a, "/mount/branch/live", "A-lazy-standard-live")
	if leaf.Path() != "/mount/branch/live" {
		t.Fatalf("initial nested Path = %q", leaf.Path())
	}

	if err := a.Del("/mount"); err != nil {
		t.Fatal(err)
	}

	if src.Path() != "" || leaf.Path() != "/branch/live" {
		t.Fatalf("detached positions = %q/%q", src.Path(), leaf.Path())
	}

	b := graftTestTree(t, "standard", "B",
		NewDirective("/a", appendProc("-a")), NewDirective("/a/b", appendProc("-b")))

	if err := b.Graft("/a/b/alias", src); err != nil {
		t.Fatal(err)
	}

	if src.Path() != "/a/b/alias" || standard.Path() != "/a/b/alias/branch" || leaf.Path() != "/a/b/alias/branch/live" {
		t.Fatalf("remounted dynamic positions = %q/%q/%q", src.Path(), standard.Path(), leaf.Path())
	}

	for i := 0; i < 3; i++ {
		graftTestGet(t, b, "/a/b/alias/branch/live", "B-a-b-lazy-standard-live")
	}

	other := graftTestTree(t, "lazy", "IGNORED", NewDirective("/", appendProc("-other")))

	if err := b.Graft("/a/b/alias/branch/live/other", other); err != nil {
		t.Fatal(err)
	}

	graftTestGet(t, b, "/a/b/alias/branch/live/other", "B-a-b-lazy-standard-live-other")
	if standard.pickChild("live") != leaf || leaf.pickChild("other") != other {
		t.Fatal("moving a mixed subtree copied or lost nested mounted nodes")
	}
}

func TestGraft_ConcurrentSetCreatesOneSharedBranch(t *testing.T) {
	dst := graftTestTree(t, "standard", "D")
	source := graftTestTree(t, "standard", "SOURCE", NewDirective("/", appendProc("-s")))

	if err := dst.Graft("/mount", source); err != nil {
		t.Fatal(err)
	}

	const workers = 32
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup

	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			<-start
			// Both the outer root and retained source handle must create nodes
			// in the same shared branch, rather than replacing each other's map.
			receiver, prefix := Tree(dst), "/mount/shared/"
			if worker%2 == 1 {
				receiver, prefix = source, "/shared/"
			}

			path := fmt.Sprintf("%sleaf-%d", prefix, worker)

			if err := receiver.Set(NewDirective(path, appendProc(fmt.Sprintf("-%d", worker)))); err != nil {
				errs <- err
			}
		}()
	}

	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}

	for worker := 0; worker < workers; worker++ {
		path := fmt.Sprintf("/mount/shared/leaf-%d", worker)
		if !dst.Has(path) {
			t.Errorf("concurrent creation lost %s", path)
			continue
		}

		graftTestGet(t, dst, path, fmt.Sprintf("D-s-%d", worker))
	}
}

func TestGraft_ConcurrentReplacementAndReads(t *testing.T) {
	dst := graftTestTree(t, "standard", "D")
	const replacements = 100
	sources := make([]*tree, replacements+1)

	for i := range sources {
		rootSuffix, leafSuffix := "-A", "-a"
		if i%2 == 1 {
			rootSuffix, leafSuffix = "-B", "-b"
		}

		sources[i] = graftTestTree(t, "lazy", "IGNORED",
			NewDirective("/", appendProc(rootSuffix)), NewDirective("/leaf", appendProc(leafSuffix)))
	}

	if err := dst.Graft("/mount", sources[0]); err != nil {
		t.Fatal(err)
	}

	const readers = 4
	start := make(chan struct{})
	errs := make(chan error, readers+1)
	var wg sync.WaitGroup

	for worker := 0; worker < readers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			<-start

			for i := 0; i < 300; i++ {
				got, err := dst.Get("/mount/leaf")
				if err != nil {
					errs <- fmt.Errorf("replacement read failed: %w", err)
					return
				}

				if string(got) != "D-A-a" && string(got) != "D-B-b" {
					errs <- fmt.Errorf("read observed incomplete replacement: %q", got)
					return
				}
			}
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()

		<-start

		for _, src := range sources[1:] {
			// Each source is used exactly once: no concurrent ownership transfer
			// or accidental remount of a source still attached elsewhere.
			if err := dst.Graft("/mount", src); err != nil {
				errs <- fmt.Errorf("replacement failed: %w", err)
				return
			}
		}
	}()

	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}
}

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

func TestGraft_ConcurrentSetAndTransferPreserveUpdates(t *testing.T) {
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
			setDone <- source.Set(NewDirective(".leaf", appendProc("-leaf")))
		}()

		go func() {
			<-start
			graftDone <- root.Graft("/slot", source)
		}()

		close(start)
		setErr, graftErr := <-setDone, <-graftDone
		if setErr != nil || graftErr != nil {
			t.Fatalf("concurrent Set/Graft errors = %v/%v", setErr, graftErr)
		}

		if root.pickChild("slot") != source || !source.Has(".leaf") {
			t.Fatal("ownership transfer lost the source identity or concurrent update")
		}

		graftTestGet(t, root, "/slot/leaf", "D-leaf")
	}
}
