package ivy

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

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

func TestGraft_ReplacementDetachesOldNativeSubtree(t *testing.T) {
	a := graftTestTree(t, "standard", "A")
	old := graftTestTree(t, "lazy", "OLD", NewDirective("/", appendProc("-old")), NewDirective("/leaf"))

	if err := a.Graft("/slot", old); err != nil {
		t.Fatal(err)
	}

	graftTestGet(t, a, "/slot/leaf", "A-old")
	replacement := graftTestTree(t, "lazy", "NEW", NewDirective("/", appendProc("-new")))

	if err := a.Graft("/slot", replacement); err != nil {
		t.Fatal(err)
	}

	if old.Path() != "" || replacement.Path() != "/slot" {
		t.Fatalf("replacement did not update ownership: old=%q new=%q", old.Path(), replacement.Path())
	}

	b := graftTestTree(t, "standard", "B")

	if err := b.Graft("/reused", old); err != nil {
		t.Fatalf("detached subtree could not be remounted: %v", err)
	}

	graftTestGet(t, a, "/slot", "A-new")
	graftTestGet(t, b, "/reused/leaf", "B-old")

	if err := b.Del("/reused/leaf"); err != nil {
		t.Fatal(err)
	}

	if old.Has("/leaf") {
		t.Fatal("remounted source no longer shared identity with destination")
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
