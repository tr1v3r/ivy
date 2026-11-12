package ivy

import (
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tr1v3r/ivy/driver"
)

// Timers bound a broken lock implementation; channels and deliberately held
// locks establish ordering. The internal-boundary unit tests additionally observe
// public TryLock results with bounded yielding, never sleeps or mutex internals.
type topologyTestTask struct {
	started   chan struct{}
	done      chan error
	collected bool
}

func startTopologyTestTask(fn func() error) *topologyTestTask {
	task := &topologyTestTask{started: make(chan struct{}), done: make(chan error, 1)}
	go func() {
		close(task.started)
		task.done <- fn()
	}()

	return task
}

func (task *topologyTestTask) await(t *testing.T, label string) (error, bool) {
	t.Helper()

	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()

	select {
	case err := <-task.done:
		task.collected = true

		return err, true
	case <-timer.C:
		t.Errorf("%s did not finish; possible topology-lock deadlock", label)

		return nil, false
	}
}

// awaitTopologyTestLock observes a lock acquired (or a writer queued) by the
// sole competing goroutine. Unlike task.started, this witnesses an actual
// internal boundary. Successful probes are immediately released.
func awaitTopologyTestLock(t *testing.T, label string, tryLock func() bool, unlock func()) bool {
	t.Helper()

	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()

	for {
		if !tryLock() {
			return true
		}
		unlock()

		select {
		case <-timer.C:
			t.Errorf("%s was not observed", label)
			return false
		default:
			runtime.Gosched()
		}
	}
}

func TestNativeTopology_UnrelatedTreesDoNotBlock(t *testing.T) {
	for _, operation := range []string{"Set", "Graft", "Del"} {
		t.Run(operation, func(t *testing.T) {
			a := graftTestTree(t, "lazy", "A", NewDirective("/old"))
			b := graftTestTree(t, "lazy", "B", NewDirective("/old"))
			sourceA := graftTestTree(t, "lazy", "SOURCE-A")
			sourceB := graftTestTree(t, "lazy", "SOURCE-B")
			run := func(target, source *tree) error {
				switch operation {
				case "Set":
					return target.Set(NewDirective("/new/leaf", appendProc("-new")))
				case "Graft":
					return target.Graft("/slot", source)
				default:
					return target.Del("/old")
				}
			}

			a.topologyMu.Lock()
			releaseA := sync.OnceFunc(a.topologyMu.Unlock)
			aTask := startTopologyTestTask(func() error { return run(a, sourceA) })
			<-aTask.started
			bTask := startTopologyTestTask(func() error { return run(b, sourceB) })
			defer func() {
				// Always release the deliberately held lock before collecting
				// either task, including timeout and assertion failure paths.
				releaseA()

				for label, task := range map[string]*topologyTestTask{"A cleanup": aTask, "B cleanup": bTask} {
					if !task.collected {
						if err, ok := task.await(t, label); ok && err != nil {
							t.Errorf("%s: %v", label, err)
						}
					}
				}
			}()

			// B must finish while A's root mutex is still held. A global
			// structural mutex, or locking unrelated roots, fails this test.
			if err, ok := bTask.await(t, "independent tree B "+operation); !ok {
				return
			} else if err != nil {
				t.Fatalf("independent tree B %s: %v", operation, err)
			}

			select {
			case err := <-aTask.done:
				aTask.collected = true
				t.Fatalf("tree A %s finished while its root mutex was held: %v", operation, err)
			default:
			}

			releaseA()
			if err, ok := aTask.await(t, "tree A after unlock "+operation); ok && err != nil {
				t.Fatalf("tree A %s: %v", operation, err)
			}
		})
	}
}

// Segment parsing remains promoted from testPathDriver, so GetLevel is first
// called by validNativeName inside Graft's locked topology validation.
type topologyBlockingDriver struct {
	*testPathDriver
	enabled atomic.Bool
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (d *topologyBlockingDriver) GetLevel(path string) int {
	if d.enabled.Load() {
		d.once.Do(func() { close(d.entered) })
		<-d.release
	}

	return d.testPathDriver.GetLevel(path)
}

func TestNativeTopology_ActualMutationDoesNotBlockUnrelatedTree(t *testing.T) {
	blockingDriver := &topologyBlockingDriver{
		testPathDriver: &testPathDriver{Modem: driver.DummyModem, DelimiterPathParser: driver.SlashPathParser},
		entered:        make(chan struct{}),
		release:        make(chan struct{}),
	}

	a, err := NewLazyTree[Directive](blockingDriver, "A", "A")
	if err != nil {
		t.Fatal(err)
	}

	b := graftTestTree(t, "lazy", "B", NewDirective("/old"))
	other := graftTestTree(t, "lazy", "OTHER")
	source := graftTestTree(t, "lazy", "SOURCE")
	blockingDriver.enabled.Store(true)
	unblockA := sync.OnceFunc(func() { close(blockingDriver.release) })
	aTask := startTopologyTestTask(func() error { return a.Graft("/new", source) })
	var bTask *topologyTestTask
	defer func() {
		unblockA()

		for label, task := range map[string]*topologyTestTask{"blocked A cleanup": aTask, "unrelated B cleanup": bTask} {
			if task != nil && !task.collected {
				if err, ok := task.await(t, label); ok && err != nil {
					t.Errorf("%s: %v", label, err)
				}
			}
		}
	}()

	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()

	select {
	case <-blockingDriver.entered:
	case <-timer.C:
		t.Fatal("A.Graft did not enter structural name validation")
	}

	// A is suspended inside a real mutation, after acquiring its actual
	// topology domain. This catches hidden global serialization even if
	// tests that hold A.topologyMu directly would otherwise miss it.
	bTask = startTopologyTestTask(func() error {
		if err := b.Set(NewDirective("/new/leaf")); err != nil {
			return err
		}

		if err := b.Graft("/slot", other); err != nil {
			return err
		}

		return b.Del("/old")
	})

	if err, ok := bTask.await(t, "B operations while A mutation is suspended"); !ok {
		return
	} else if err != nil {
		t.Fatal(err)
	}

	if !b.Has("/new/leaf") || !b.Has("/slot") || b.Has("/old") {
		t.Fatal("B operations did not complete before releasing A")
	}

	select {
	case err := <-aTask.done:
		aTask.collected = true
		t.Fatalf("A.Graft completed before its validation barrier was released: %v", err)
	default:
	}

	unblockA()
	if err, ok := aTask.await(t, "A.Graft after releasing validation barrier"); ok && err != nil {
		t.Fatal(err)
	}
}

func TestNativeTopology_MountedNodesUseEnclosingRootDomain(t *testing.T) {
	root := graftTestTree(t, "lazy", "ROOT")
	source := graftTestTree(t, "lazy", "SOURCE", NewDirective("/old"))
	other := graftTestTree(t, "lazy", "OTHER")

	if err := root.Graft("/mounted", source); err != nil {
		t.Fatal(err)
	}

	// The mounted node's own zero-value mutex is dormant. Only the enclosing
	// root's mutex is the topology domain; there is no field migration.
	source.topologyMu.Lock()
	releaseSource := sync.OnceFunc(source.topologyMu.Unlock)
	task := startTopologyTestTask(func() error {
		if err := source.Set(NewDirective("/new/leaf")); err != nil {
			return err
		}

		if err := source.Graft("/new/other", other); err != nil {
			return err
		}

		return root.Del("/mounted/old")
	})

	defer func() {
		releaseSource()
		if !task.collected {
			if err, ok := task.await(t, "mounted node cleanup"); ok && err != nil {
				t.Errorf("mounted node cleanup: %v", err)
			}
		}
	}()

	if err, ok := task.await(t, "mounted operations with dormant mutex held"); !ok {
		return
	} else if err != nil {
		t.Fatal(err)
	}

	if !root.Has("/mounted/new/leaf") || root.Has("/mounted/old") || other.Path() != "/mounted/new/other" {
		t.Fatal("operations did not mutate the enclosing tree through the mounted handle")
	}
}

func TestNativeTopology_ReciprocalGraftsDoNotDeadlock(t *testing.T) {
	for iteration := 0; iteration < 32; iteration++ {
		a := graftTestTree(t, "lazy", "A")
		b := graftTestTree(t, "lazy", "B")
		start := make(chan struct{})
		aTask := startTopologyTestTask(func() error {
			<-start

			return a.Graft("/b", b)
		})

		bTask := startTopologyTestTask(func() error {
			<-start

			return b.Graft("/a", a)
		})

		close(start)
		aErr, aOK := aTask.await(t, "A grafting B")
		bErr, bOK := bTask.await(t, "B grafting A")
		if !aOK || !bOK {
			return
		}

		if (aErr == nil) == (bErr == nil) {
			t.Fatalf("reciprocal grafts need exactly one winner: A=%v B=%v", aErr, bErr)
		}

		loserErr := aErr
		if loserErr == nil {
			loserErr = bErr
		}

		if !strings.Contains(loserErr.Error(), "cycle") {
			t.Fatalf("reciprocal loser did not reject the cycle: %v", loserErr)
		}

		if aErr == nil {
			if a.pickChild("b") != b || a.mount.Load() != nil || b.mount.Load().parent != a {
				t.Fatal("A winner has inconsistent ownership")
			}
		} else if b.pickChild("a") != a || b.mount.Load() != nil || a.mount.Load().parent != b {
			t.Fatal("B winner has inconsistent ownership")
		}
	}
}

func TestNativeTopology_CompetingTargetsHaveOneSourceOwner(t *testing.T) {
	for iteration := 0; iteration < 32; iteration++ {
		a := graftTestTree(t, "lazy", "A")
		b := graftTestTree(t, "lazy", "B")
		source := graftTestTree(t, "lazy", "SOURCE", NewDirective("/leaf"))
		start := make(chan struct{})
		aTask := startTopologyTestTask(func() error {
			<-start

			return a.Graft("/x/y/slot", source)
		})

		bTask := startTopologyTestTask(func() error {
			<-start

			return b.Graft("/x/y/slot", source)
		})

		close(start)
		aErr, aOK := aTask.await(t, "A claiming source")
		bErr, bOK := bTask.await(t, "B claiming source")
		if !aOK || !bOK {
			return
		}

		if (aErr == nil) == (bErr == nil) {
			t.Fatalf("competing targets need exactly one winner: A=%v B=%v", aErr, bErr)
		}

		winner, loser := a, b
		if bErr == nil {
			winner, loser = b, a
		}

		if !winner.Has("/x/y/slot/leaf") || loser.Has("/x") {
			t.Fatal("winner lost the created path or rejected claim published a prefix")
		}

		parent := winner.pickChild("x").(*tree).pickChild("y").(*tree)
		link := source.mount.Load()
		if link == nil || link.parent != parent || link.name != "slot" || parent.pickChild("slot") != source {
			t.Fatal("source ownership disagrees with the single successful target")
		}
	}
}

func TestNativeTopology_LockHelperReacquiresMovedReceiverRoot(t *testing.T) {
	witness := graftTestTree(t, "lazy", "WITNESS")
	oldRoot := graftTestTree(t, "lazy", "OLD")
	// Match the production lock order without inspecting mutex internals.
	if reflect.ValueOf(witness).Pointer() > reflect.ValueOf(oldRoot).Pointer() {
		witness, oldRoot = oldRoot, witness
	}
	receiver := graftTestTree(t, "lazy", "RECEIVER")
	if err := oldRoot.Graft("/receiver", receiver); err != nil {
		t.Fatal(err)
	}

	oldRoot.topologyMu.Lock()
	releaseOld := sync.OnceFunc(oldRoot.topologyMu.Unlock)
	acquired, release := make(chan struct{}), make(chan struct{})
	releaseTask := sync.OnceFunc(func() { close(release) })
	task := startTopologyTestTask(func() error {
		unlock := lockTopologyRoots(receiver, witness)
		defer unlock()
		close(acquired)
		<-release
		return nil
	})
	defer func() {
		releaseOld()
		releaseTask()
		if !task.collected {
			if err, ok := task.await(t, "moved receiver cleanup"); ok && err != nil {
				t.Error(err)
			}
		}
	}()

	// All roots are snapshotted before the first lock is acquired. Seeing
	// witness locked proves receiver was resolved to oldRoot, and the caller
	// cannot pass oldRoot's lock until we release it. A started channel alone
	// would not establish either fact.
	if !awaitTopologyTestLock(t, "first root acquisition", witness.topologyMu.TryLock, witness.topologyMu.Unlock) {
		return
	}

	// UNIT isolation: publish just the detach edge under the old topology
	// domain and parent node lock. Public Del would recursively take the root
	// lock we deliberately hold; no cache or realization state is changed.
	oldRoot.mu.Lock()
	delete(oldRoot.children, "receiver")
	receiver.mount.Store(nil)
	oldRoot.mu.Unlock()
	releaseOld()

	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-acquired:
	case <-timer.C:
		t.Fatal("helper did not reacquire the moved receiver's current root")
	}

	if receiver.topologyMu.TryLock() {
		receiver.topologyMu.Unlock()
		t.Error("helper returned without holding the detached receiver's current root")
	}
	if !oldRoot.topologyMu.TryLock() {
		t.Error("helper retained the stale old-root lock")
	} else {
		oldRoot.topologyMu.Unlock()
	}
	if witness.topologyMu.TryLock() {
		witness.topologyMu.Unlock()
		t.Error("helper lost the other requested root during retry")
	}

	releaseTask()
	if err, ok := task.await(t, "moved receiver after release"); ok && err != nil {
		t.Fatal(err)
	}
}

func TestNativeTopology_GraftRejectsOwnershipChangedAfterPrecheck(t *testing.T) {
	target := graftTestTree(t, "lazy", "TARGET")
	owner := graftTestTree(t, "lazy", "OWNER")
	source := graftTestTree(t, "lazy", "SOURCE")

	source.realizeMu.RLock()
	releaseRead := sync.OnceFunc(source.realizeMu.RUnlock)
	task := startTopologyTestTask(func() error { return target.Graft("/new/slot", source) })
	defer func() {
		releaseRead()
		if !task.collected {
			if err, ok := task.await(t, "late ownership cleanup"); ok && err != nil {
				t.Error(err)
			}
		}
	}()

	// With our read lock held, a failed TryRLock witnesses Graft's pending
	// writer, which is after the detached-source fast precheck. No ownership
	// has changed yet, so only the later locked recheck can reject it.
	if !awaitTopologyTestLock(t, "source realization writer pending", source.realizeMu.TryRLock, source.realizeMu.RUnlock) {
		return
	}

	// UNIT isolation of the late ownership check: install only an ownership
	// edge with both root domains and the parent node locked. A second public
	// Graft cannot do this while our realization read lock is held. We do not
	// claim otherwise or touch realization/cache fields under a read lock.
	unlock := lockTopologyRoots(owner, source)
	owner.mu.Lock()
	owner.children["claimed"] = source
	source.mount.Store(&treeMount{parent: owner, name: "claimed"})
	owner.mu.Unlock()
	unlock()
	releaseRead()

	err, ok := task.await(t, "Graft after source ownership changed")
	if !ok {
		return
	}
	if err == nil || !strings.Contains(err.Error(), "already attached") {
		t.Fatalf("Graft did not reject late native ownership: %v", err)
	}
	if target.Has("/new") {
		t.Error("rejected Graft published missing target parents")
	}
	link := source.mount.Load()
	if link == nil || link.parent != owner || link.name != "claimed" || owner.pickChild("claimed") != source {
		t.Error("rejected Graft changed the established source owner")
	}
}

func TestNativeTopology_LockHelperDeduplicatesCurrentRoot(t *testing.T) {
	root := graftTestTree(t, "lazy", "ROOT", NewDirective("/child"))
	child := root.pickChild("child").(*tree)
	acquired, release := make(chan struct{}), make(chan struct{})
	releaseTask := sync.OnceFunc(func() { close(release) })
	task := startTopologyTestTask(func() error {
		unlock := lockTopologyRoots(root, child, root, child)
		close(acquired)
		<-release
		unlock()

		return nil
	})

	defer func() {
		releaseTask()
		if !task.collected {
			if err, ok := task.await(t, "lock helper cleanup"); ok && err != nil {
				t.Errorf("lock helper cleanup: %v", err)
			}
		}
	}()

	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()

	select {
	case <-acquired:
	case <-timer.C:
		t.Fatal("helper did not deduplicate nodes sharing the same root")
	}

	if root.topologyMu.TryLock() {
		root.topologyMu.Unlock()
		t.Error("helper did not hold the enclosing root mutex")
	}

	if !child.topologyMu.TryLock() {
		t.Error("helper locked a mounted node's dormant mutex")
	} else {
		child.topologyMu.Unlock()
	}

	releaseTask()
	if err, ok := task.await(t, "lock helper after release"); ok && err != nil {
		t.Fatal(err)
	}
}
