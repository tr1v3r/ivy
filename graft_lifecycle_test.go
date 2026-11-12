package ivy

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

func TestGraft_ConcurrentMutationsCreateOneSharedBranch(t *testing.T) {
	dst := graftTestTree(t, "standard", "D")
	source := graftTestTree(t, "standard", "SOURCE", NewDirective("/", appendProc("-s")))

	if err := dst.Graft("/mount", source); err != nil {
		t.Fatal(err)
	}

	const workers = 32
	sources := make([]Tree, workers)
	for worker := range sources {
		if worker%3 == 0 {
			sources[worker] = graftTestTree(t, "lazy", "IGNORED", NewDirective("/", appendProc(fmt.Sprintf("-%d", worker))))
		}
	}

	start := make(chan struct{})
	tasks := make([]*topologyTestTask, 0, workers)

	for worker := 0; worker < workers; worker++ {
		tasks = append(tasks, startTopologyTestTask(func() error {
			<-start
			// Both the outer root and retained source handle must create nodes
			// in the same shared branch, rather than replacing each other's map.
			receiver, prefix := Tree(dst), "/mount/shared/"
			if worker%2 == 1 {
				receiver, prefix = source, "/shared/"
			}

			path := fmt.Sprintf("%sleaf-%d", prefix, worker)

			var err error
			if sources[worker] != nil {
				err = receiver.Graft(path, sources[worker])
			} else {
				err = receiver.Set(NewDirective(path, appendProc(fmt.Sprintf("-%d", worker))))
			}

			return err
		}))
	}

	close(start)
	complete := true
	for _, task := range tasks {
		err, ok := task.await(t, "concurrent shared-branch mutation")
		complete = complete && ok
		if err != nil {
			t.Error(err)
		}
	}
	if !complete {
		return
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
	tasks := make([]*topologyTestTask, 0, readers+1)

	for range readers {
		tasks = append(tasks, startTopologyTestTask(func() error {
			<-start
			for range 300 {
				got, err := dst.Get("/mount/leaf")
				if err != nil {
					return fmt.Errorf("replacement read failed: %w", err)
				}

				if string(got) != "D-A-a" && string(got) != "D-B-b" {
					return fmt.Errorf("read observed incomplete replacement: %q", got)
				}
			}

			return nil
		}))
	}

	tasks = append(tasks, startTopologyTestTask(func() error {
		<-start
		for _, src := range sources[1:] {
			// Each source is used exactly once; public same-source transfer and
			// retry behavior is covered by PublicQueriesRetryAcrossRemount.
			if err := dst.Graft("/mount", src); err != nil {
				return fmt.Errorf("replacement failed: %w", err)
			}
		}

		return nil
	}))

	close(start)
	for _, task := range tasks {
		if err, ok := task.await(t, "concurrent replacement and read"); ok && err != nil {
			t.Error(err)
		}
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

func awaitGraftSignal(t *testing.T, signal <-chan struct{}, label string) bool {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()

	select {
	case <-signal:
		return true
	case <-timer.C:
		t.Errorf("%s did not finish", label)
		return false
	}
}

func TestGraft_ParentChainPublicationWithConcurrentContextReads(t *testing.T) {
	const path = "/x/y/z/api/leaf"
	root := graftTestTree(t, "standard", "P ${user}")
	capture := &driver.RawProcessor{Proc: func(rc *driver.RealizeContext, before []byte) ([]byte, error) {
		if rc.TreePath != path || string(rc.ParentContent) != "P ${user}-s" {
			return nil, fmt.Errorf("published-chain metadata = %q/%q", rc.TreePath, rc.ParentContent)
		}

		return before, nil
	}}
	source := graftTestTree(t, "lazy", "SOURCE", NewDirective("/", appendProc("-s")),
		NewDirective("/leaf", appendProc("-l"), &driver.TemplateProcessor{}, capture))

	// Stop Graft at cache preparation: missing parents have been constructed,
	// but the final source edge has not been attached. No test hook is needed.
	source.contentMu.Lock()
	releaseContent := sync.OnceFunc(source.contentMu.Unlock)
	var tasks []*topologyTestTask
	defer func() {
		releaseContent()
		for _, task := range tasks {
			if !task.collected {
				if err, ok := task.await(t, "parent publication cleanup"); ok && err != nil {
					t.Error(err)
				}
			}
		}
	}()

	graftTask := startTopologyTestTask(func() error { return root.Graft("/x/y/z/api", source) })
	tasks = append(tasks, graftTask)
	deadline := time.Now().Add(5 * time.Second)
	for !source.basePending.Load() {
		if time.Now().After(deadline) {
			t.Fatal("Graft did not reach cache preparation")
		}

		runtime.Gosched()
	}

	// This observable partial-publication window must still be protected by
	// the original parent's write lock, not merely by the topology mutex.
	if root.mu.TryRLock() {
		root.mu.RUnlock()
		t.Fatal("readers can enter the new parent chain before source attachment")
	}

	readers := []*topologyTestTask{startTopologyTestTask(func() error {
		if !root.Has(path) {
			return fmt.Errorf("Has observed a parent chain without its source")
		}

		return nil
	})}
	for _, user := range []string{"alice", "bob"} {
		readers = append(readers, startTopologyTestTask(func() error {
			rc := paramRC(map[string]string{"user": user})
			got, err := root.GetWithContext(rc, path)
			if err != nil {
				return fmt.Errorf("concurrent published query %s: %w", user, err)
			}
			if string(got) != "P "+user+"-s-l" {
				return fmt.Errorf("concurrent published query %s = %q", user, got)
			}

			return nil
		}))
	}
	tasks = append(tasks, readers...)
	for _, reader := range readers {
		if !awaitGraftSignal(t, reader.started, "publication reader started") {
			return
		}
	}

	releaseContent()
	complete := true
	for _, task := range tasks {
		err, ok := task.await(t, "complete parent chain and concurrent readers")
		complete = complete && ok
		if err != nil {
			t.Error(err)
		}
	}
	if complete {
		graftTestGet(t, root, path, "P ${user}-s-l")
	}
}

func TestGraft_PublicQueriesRetryAcrossRemount(t *testing.T) {
	for _, contextual := range []bool{false, true} {
		for _, leafReceiver := range []bool{false, true} {
			t.Run(fmt.Sprintf("context=%v/leaf=%v", contextual, leafReceiver), func(t *testing.T) {
				const mountedPath = "/new/deep/slot"
				oldEntered, releaseOld := make(chan struct{}), make(chan struct{})
				freshEntered, releaseFresh := make(chan struct{}), make(chan struct{})
				unblockOld := sync.OnceFunc(func() { close(releaseOld) })
				unblockFresh := sync.OnceFunc(func() { close(releaseFresh) })
				markOld := sync.OnceFunc(func() { close(oldEntered) })
				markFresh := sync.OnceFunc(func() { close(freshEntered) })
				var blockOld atomic.Bool
				a := graftTestTree(t, "instant", "A ${user}", NewDirective("/", &driver.RawProcessor{
					Proc: func(_ *driver.RealizeContext, before []byte) ([]byte, error) {
						if blockOld.Load() {
							markOld()
							<-releaseOld
						}

						return before, nil
					},
				}))
				for range 10 {
					graftTestGet(t, a, "/", "A ${user}")
				}
				_, oldVersion := a.getWithVersion()

				var sourceRuns atomic.Int32
				capture := &driver.RawProcessor{Proc: func(rc *driver.RealizeContext, before []byte) ([]byte, error) {
					if rc.TreePath != mountedPath+"/leaf" || string(rc.ParentContent) != "B ${user}-s" {
						return nil, fmt.Errorf("dynamic metadata = %q/%q", rc.TreePath, rc.ParentContent)
					}

					if rc.Params["user"] == "fresh" {
						markFresh()
						<-releaseFresh
					}

					return before, nil
				}}
				source := graftTestTree(t, "lazy", "SOURCE",
					NewDirective("/", graftTestCountProc(&sourceRuns, "-s")),
					NewDirective("/leaf", appendProc("-l"), &driver.TemplateProcessor{}, capture))
				if err := a.Graft("/slot", source); err != nil {
					t.Fatal(err)
				}

				b := graftTestTree(t, "standard", "B ${user}")
				receiver, path := Tree(source), "/leaf"
				if leafReceiver {
					receiver, path = source.pickChild("leaf"), "/"
				}

				var oldTask, moveTask, freshTask *topologyTestTask
				defer func() {
					unblockOld()
					unblockFresh()
					for _, task := range []*topologyTestTask{oldTask, moveTask, freshTask} {
						if task != nil && !task.collected {
							if err, ok := task.await(t, "remount query cleanup"); ok && err != nil {
								t.Error(err)
							}
						}
					}
				}()

				oldRC := paramRC(map[string]string{"user": "old"})
				blockOld.Store(true)
				oldTask = startTopologyTestTask(func() error {
					var got []byte
					var err error
					want := "B ${user}-s-l"
					if contextual {
						got, err = receiver.GetWithContext(oldRC, path)
						want = "B old-s-l"
					} else {
						got, err = receiver.Get(path)
					}

					if err != nil {
						return fmt.Errorf("public remount read: %w", err)
					}
					if string(got) != want {
						return fmt.Errorf("public remount read = %q; want %q", got, want)
					}

					return nil
				})
				if !awaitGraftSignal(t, oldEntered, "query reaching the old parent") {
					return
				}

				moveTask = startTopologyTestTask(func() error {
					if err := a.Del("/slot"); err != nil {
						return err
					}

					return b.Graft(mountedPath, source)
				})
				if err, ok := moveTask.await(t, "source remount while old query is suspended"); !ok || err != nil {
					t.Errorf("remount: %v", err)
					return
				}

				freshTask = startTopologyTestTask(func() error {
					got, err := b.GetWithContext(paramRC(map[string]string{"user": "fresh"}), mountedPath+"/leaf")
					if err != nil {
						return fmt.Errorf("new-parent query: %w", err)
					}
					if string(got) != "B fresh-s-l" {
						return fmt.Errorf("new-parent query = %q", got)
					}

					return nil
				})
				if !awaitGraftSignal(t, freshEntered, "new parent cached with a concurrent dynamic query") {
					return
				}

				unblockOld()
				if err, ok := oldTask.await(t, "public query retry on the new parent"); !ok || err != nil {
					t.Errorf("retried query: %v", err)
					return
				}
				unblockFresh()
				if err, ok := freshTask.await(t, "concurrent parameter query"); !ok || err != nil {
					t.Errorf("fresh query: %v", err)
					return
				}

				if contextual && (oldRC.TreePath != mountedPath+"/leaf" || string(oldRC.ParentContent) != "B ${user}-s" || oldRC.Params["user"] != "old") {
					t.Fatalf("retried context = %#v", oldRC)
				}
				if source.realizedFrom >= oldVersion || sourceRuns.Load() != 1 {
					t.Fatal("old parent's high version overwrote or re-realized the new parent cache")
				}

				graftTestGet(t, b, mountedPath+"/leaf", "B ${user}-s-l")
				got, err := source.GetWithContext(paramRC(map[string]string{"user": "last"}), "/leaf")
				if err != nil || string(got) != "B last-s-l" {
					t.Fatalf("parameter-isolated cache after remount = %q, %v", got, err)
				}
			})
		}
	}
}

func TestGraft_PublicQueryRetriesAncestorDetach(t *testing.T) {
	for _, contextual := range []bool{false, true} {
		t.Run(fmt.Sprint(contextual), func(t *testing.T) {
			root := graftTestTree(t, "standard", "A")
			var runs atomic.Int32
			source := graftTestTree(t, "lazy", "SOURCE", NewDirective("/", &driver.RawProcessor{
				Proc: func(_ *driver.RealizeContext, before []byte) ([]byte, error) {
					runs.Add(1)
					if err := root.Del("/slot"); err != nil {
						return nil, err
					}

					return append(before, "-s"...), nil
				},
			}), NewDirective("/leaf", appendProc("-l")))
			if err := root.Graft("/slot", source); err != nil {
				t.Fatal(err)
			}

			leaf := source.pickChild("leaf")
			var got []byte
			var err error
			if contextual {
				got, err = leaf.GetWithContext(paramRC(nil), "/")
			} else {
				got, err = leaf.Get("/")
			}

			if err != nil || string(got) != "A-s-l" || runs.Load() != 1 || source.Path() != "" || root.Has("/slot") {
				t.Fatalf("read after ancestor detached itself = %q, %v; runs=%d path=%q", got, err, runs.Load(), source.Path())
			}
		})
	}
}

func TestGraft_PublicQueryPropagatesAncestorError(t *testing.T) {
	wantErr := errors.New("ancestor failed")
	for _, contextual := range []bool{false, true} {
		t.Run(fmt.Sprint(contextual), func(t *testing.T) {
			root := graftTestTree(t, "lazy", "A", NewDirective("/", &driver.RawProcessor{
				Proc: func(_ *driver.RealizeContext, _ []byte) ([]byte, error) { return nil, wantErr },
			}))
			source := graftTestTree(t, "lazy", "SOURCE")
			if err := root.Graft("/slot", source); err != nil {
				t.Fatal(err)
			}

			var err error
			if contextual {
				_, err = source.GetWithContext(paramRC(nil), "/")
			} else {
				_, err = source.Get("/")
			}

			if !errors.Is(err, wantErr) || !source.basePending.Load() {
				t.Fatalf("ancestor error = %v; source pending=%v", err, source.basePending.Load())
			}
		})
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
		setTask := startTopologyTestTask(func() error {
			<-start
			return source.Set(NewDirective(".leaf", appendProc("-leaf")))
		})
		graftTask := startTopologyTestTask(func() error {
			<-start
			return root.Graft("/slot", source)
		})

		close(start)
		setErr, setOK := setTask.await(t, "Set racing with source transfer")
		graftErr, graftOK := graftTask.await(t, "source transfer racing with Set")
		if !setOK || !graftOK {
			return
		}
		if setErr != nil || graftErr != nil {
			t.Fatalf("concurrent Set/Graft errors = %v/%v", setErr, graftErr)
		}

		if root.pickChild("slot") != source || !source.Has(".leaf") {
			t.Fatal("ownership transfer lost the source identity or concurrent update")
		}

		graftTestGet(t, root, "/slot/leaf", "D-leaf")
	}
}
