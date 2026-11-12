package ivy

import (
	"bytes"
	"fmt"
	"sync/atomic"
	"testing"

	"golang.org/x/time/rate"

	"github.com/tr1v3r/ivy/driver"
)

type graftTestSourceDriver struct {
	*testPathDriver
	calls *atomic.Int32
}

func (*graftTestSourceDriver) Name() string { return "graft-source" }

func (d *graftTestSourceDriver) Realize(rc *driver.RealizeContext, before []byte, procs ...driver.Processor) ([]byte, error) {
	d.calls.Add(1)

	return d.StdRealizer.Realize(rc, before, procs...)
}

func TestGraft_NativeSourceKeepsDriverWithRootPathSyntax(t *testing.T) {
	var calls atomic.Int32
	sourceDriver := &graftTestSourceDriver{
		testPathDriver: &testPathDriver{Modem: driver.DummyModem, DelimiterPathParser: new(driver.DelimiterPathParser).WithDelimiter(".")},
		calls:          &calls,
	}

	src, err := NewLazyTree(sourceDriver, "source", "SOURCE", NewDirective(".", appendProc("-s")), NewDirective(".leaf", appendProc("-l")))
	if err != nil {
		t.Fatal(err)
	}

	dst := graftTestTree(t, "standard", "D", NewDirective("/parent", appendProc("-p")))

	if err := dst.Graft("/parent/alias", src); err != nil {
		t.Fatal(err)
	}

	if src.(*tree).driver != sourceDriver {
		t.Fatal("Graft replaced source driver instead of retaining format execution")
	}

	graftTestGet(t, dst, "/parent/alias/leaf", "D-p-s-l")
	if calls.Load() != 2 {
		t.Fatalf("source realizer calls = %d; want 2", calls.Load())
	}

	if err := dst.Set(NewDirective("/parent/alias/added", appendProc("-a"))); err != nil {
		t.Fatal(err)
	}

	graftTestGet(t, dst, "/parent/alias/added", "D-p-s-a")
	if !dst.Has("/parent/alias/added") {
		t.Fatal("Has switched from root parser to source parser")
	}

	if err := dst.Del("/parent/alias/added"); err != nil {
		t.Fatal(err)
	}

	if dst.Has("/parent/alias/added") {
		t.Fatal("Del switched from root parser to source parser")
	}

	other := graftTestTree(t, "lazy", "IGNORED", NewDirective("/", appendProc("-other")))

	if err := dst.Graft("/parent/alias/leaf/other", other); err != nil {
		t.Fatal(err)
	}

	graftTestGet(t, dst, "/parent/alias/leaf/other", "D-p-s-l-other")
	if other.Path() != "/parent/alias/leaf/other" {
		t.Fatalf("Path did not use root syntax: %q", other.Path())
	}
}

func TestGraft_CustomRootParserControlsNativeTraversal(t *testing.T) {
	rootDriver := &testPathDriver{Modem: driver.DummyModem, DelimiterPathParser: new(driver.DelimiterPathParser).WithDelimiter(".")}
	root, err := NewLazyTree(rootDriver, "root", "R", NewDirective(".parent", appendProc("-p")))
	if err != nil {
		t.Fatal(err)
	}

	source := graftTestTree(t, "lazy", "IGNORED", NewDirective("/", appendProc("-s")), NewDirective("/leaf", appendProc("-l")))

	if err := root.Graft(".parent.alias", source); err != nil {
		t.Fatal(err)
	}

	graftTestGet(t, root, ".parent.alias.leaf", "R-p-s-l")
	if source.Path() != ".parent.alias" || source.pickChild("leaf").Path() != ".parent.alias.leaf" {
		t.Fatalf("mounted paths ignored root parser: %q/%q", source.Path(), source.pickChild("leaf").Path())
	}

	if err := root.Set(NewDirective(".parent.alias.added", appendProc("-a"))); err != nil {
		t.Fatal(err)
	}

	graftTestGet(t, root, ".parent.alias.added", "R-p-s-a")
	if !root.Has(".parent.alias.added") {
		t.Fatal("custom root Has did not consume local segments")
	}

	if err := root.Del(".parent.alias.added"); err != nil {
		t.Fatal(err)
	}

	if root.Has(".parent.alias.added") {
		t.Fatal("custom root Del did not consume local segments")
	}
}

// This intentionally does not embed a native tree: every public operation is
// overridden so a promoted Get/Set cannot conceal an incorrect full-path call.
type graftTestForeignTree struct {
	calls       map[string]string
	setProcs    []driver.Processor
	graftSource Tree
	params      map[string]string
}

func newGraftTestForeignTree() *graftTestForeignTree {
	return &graftTestForeignTree{calls: make(map[string]string)}
}

func (*graftTestForeignTree) Name() string { return "foreign-identity" }

func (*graftTestForeignTree) Path() string { return "" }

func (f *graftTestForeignTree) Get(path string) ([]byte, error) {
	f.calls["Get"] = path

	return []byte("foreign:" + path), nil
}

func (f *graftTestForeignTree) GetWithContext(rc *driver.RealizeContext, path string) ([]byte, error) {
	f.calls["GetWithContext"] = path

	if rc != nil {
		f.params = rc.Params
	}

	return []byte("foreign-context:" + path), nil
}

func (f *graftTestForeignTree) Set(d Directive) error {
	f.calls["Set"] = d.Path()
	f.setProcs = d.Processors()

	return nil
}

func (f *graftTestForeignTree) Has(path string) bool {
	f.calls["Has"] = path

	return true
}

func (f *graftTestForeignTree) Del(path string) error {
	f.calls["Del"] = path

	return nil
}

func (f *graftTestForeignTree) Graft(path string, source Tree) error {
	f.calls["Graft"] = path
	f.graftSource = source

	return nil
}

func (*graftTestForeignTree) ShowStruct() []byte { return []byte(`{}`) }

func (*graftTestForeignTree) SetRateLimit(rate.Limit, int) {}

func (*graftTestForeignTree) SetFallback(driver.Processor) {}

func (*graftTestForeignTree) SetDefaultContext(*driver.RealizeContext) {}

func TestGraft_ForeignTreeReceivesOnlyRemainingLocalPaths(t *testing.T) {
	dst := graftTestTree(t, "standard", "D", NewDirective("/parent"))
	foreign := newGraftTestForeignTree()

	if err := dst.Graft("/parent/alias", foreign); err != nil {
		t.Fatal(err)
	}

	graftTestGet(t, dst, "/parent/alias/leaf/child", "foreign:/leaf/child")
	got, err := dst.GetWithContext(paramRC(map[string]string{"user": "alice"}), "/parent/alias/leaf/child")
	if err != nil || string(got) != "foreign-context:/leaf/child" || foreign.params["user"] != "alice" {
		t.Fatalf("foreign context query = %q, %v, params=%v", got, err, foreign.params)
	}

	proc := appendProc("-set")

	if err := dst.Set(NewDirective("/parent/alias/leaf/child", proc)); err != nil {
		t.Fatal(err)
	}

	if len(foreign.setProcs) != 1 || foreign.setProcs[0] != proc {
		t.Fatal("local directive lost processors")
	}

	if !dst.Has("/parent/alias/leaf/child") {
		t.Fatal("foreign Has result was lost")
	}

	if err := dst.Del("/parent/alias/leaf/child"); err != nil {
		t.Fatal(err)
	}

	other := graftTestTree(t, "lazy", "OTHER")

	if err := dst.Graft("/parent/alias/leaf/child", other); err != nil {
		t.Fatal(err)
	}

	for _, method := range []string{"Get", "GetWithContext", "Set", "Has", "Del", "Graft"} {
		if got := foreign.calls[method]; got != "/leaf/child" {
			t.Errorf("foreign %s received %q; want local /leaf/child", method, got)
		}
	}

	if foreign.graftSource != other {
		t.Fatal("foreign Graft did not receive original source object")
	}
}

func graftDispatchSource(t *testing.T, native bool) Tree {
	t.Helper()

	if !native {
		return newGraftTestForeignTree()
	}

	source := graftTestTree(t, "standard", "SOURCE",
		NewDirective("/", appendProc("-source")), NewDirective("/leaf"))
	graftTestGet(t, source, "/leaf", "SOURCE-source")

	return source
}

func TestGraft_SourceTargetDispatchMatrix(t *testing.T) {
	for _, sourceKind := range []string{"native", "custom"} {
		for _, targetKind := range []string{"native", "custom"} {
			t.Run(sourceKind+"_source_"+targetKind+"_target", func(t *testing.T) {
				root := graftTestTree(t, "standard", "ROOT", NewDirective("/parent"))
				parent := root.pickChild("parent").(*tree)
				source := graftDispatchSource(t, sourceKind == "native")

				if targetKind == "custom" {
					target := newGraftTestForeignTree()
					if err := root.Graft("/parent/boundary", target); err != nil {
						t.Fatal(err)
					}

					var cached []byte
					var version uint64
					if nativeSource, ok := source.(*tree); ok {
						cached, version = nativeSource.getWithVersion()
						cached = bytes.Clone(cached)
					}

					if err := root.Graft("/parent/boundary/a//b", source); err != nil {
						t.Fatal(err)
					}

					if parent.pickChild("boundary") != target {
						t.Fatal("delegation replaced the custom target instead of entering it")
					}

					if target.calls["Graft"] != "/a//b" || target.graftSource != source {
						t.Fatalf("custom target received path %q and source %p; want /a//b and %p", target.calls["Graft"], target.graftSource, source)
					}

					if nativeSource, ok := source.(*tree); ok {
						current, currentVersion := nativeSource.getWithVersion()
						if !bytes.Equal(current, cached) || currentVersion != version || nativeSource.basePending.Load() || nativeSource.realizedAt.IsZero() {
							t.Fatal("delegating into a custom target changed native source cache before target chose to mount")
						}

						if nativeSource.mount.Load() != nil {
							t.Fatal("record-only custom target unexpectedly attached the native source")
						}
					} else if len(source.(*graftTestForeignTree).calls) != 0 {
						t.Fatal("delegation invoked operations on the custom source")
					}

					return
				}

				previous := graftTestTree(t, "lazy", "PREVIOUS", NewDirective("/", appendProc("-old")), NewDirective("/old"))
				if err := root.Graft("/parent/slot", previous); err != nil {
					t.Fatal(err)
				}

				graftTestGet(t, root, "/parent/slot/old", "ROOT-old")

				if err := root.Graft("/parent/slot", source); err != nil {
					t.Fatal(err)
				}

				if parent.pickChild("slot") != source {
					t.Fatal("native target did not attach the exact source object")
				}

				if previous.mount.Load() != nil || previous.Path() != "" {
					t.Fatal("replacing an existing native child did not detach it")
				}

				if nativeSource, ok := source.(*tree); ok {
					if root.Has("/parent/slot/old") {
						t.Fatal("replacement retained an old descendant")
					}

					if nativeSource.Path() != "/parent/slot" {
						t.Fatalf("native source Path = %q", nativeSource.Path())
					}

					graftTestGet(t, root, "/parent/slot/leaf", "ROOT-source")
				} else {
					customSource := source.(*graftTestForeignTree)
					if len(customSource.calls) != 0 {
						t.Fatal("native target invoked custom source operations during direct insertion")
					}

					graftTestGet(t, root, "/parent/slot/leaf", "foreign:/leaf")
				}

				reused := graftTestTree(t, "standard", "REUSED")
				if err := reused.Graft("/slot", previous); err != nil {
					t.Fatalf("replaced subtree could not be remounted: %v", err)
				}

				graftTestGet(t, reused, "/slot/old", "REUSED-old")
				if err := reused.Del("/slot/old"); err != nil {
					t.Fatal(err)
				}

				if previous.Has("/old") {
					t.Fatal("remounted subtree lost identity with the destination")
				}
			})
		}
	}
}

// This target forwards synchronously into a separate native root. Checking
// TryLock before forwarding turns a retained-lock regression into an error,
// rather than letting the test hang while the forwarded Graft reacquires it.
type graftDispatchForwardingTarget struct {
	*graftTestForeignTree
	outerRoot       *tree
	innerRoot       *tree
	expectedContent []byte
	expectedVersion uint64
	locksReleased   bool
}

func (target *graftDispatchForwardingTarget) Graft(path string, source Tree) error {
	target.calls["Graft"] = path
	target.graftSource = source

	if !target.outerRoot.topologyMu.TryLock() {
		return fmt.Errorf("outer root topology lock is still held during custom delegation")
	}

	target.outerRoot.topologyMu.Unlock()

	if nativeSource, ok := source.(*tree); ok {
		if !nativeSource.realizeMu.TryLock() {
			return fmt.Errorf("native source realization lock is still held during custom delegation")
		}

		nativeSource.realizeMu.Unlock()

		if !nativeSource.topologyMu.TryLock() {
			return fmt.Errorf("native source topology lock is still held during custom delegation")
		}

		nativeSource.topologyMu.Unlock()

		content, version := nativeSource.getWithVersion()
		if !bytes.Equal(content, target.expectedContent) || version != target.expectedVersion || nativeSource.basePending.Load() || nativeSource.realizedAt.IsZero() {
			return fmt.Errorf("native source cache changed before the custom destination mounted it")
		}
	}

	target.locksReleased = true

	return target.innerRoot.Graft(path, source)
}

func TestGraft_CustomTargetCanForwardAfterLocksReleased(t *testing.T) {
	for _, sourceKind := range []string{"native", "custom"} {
		t.Run(sourceKind+"_source", func(t *testing.T) {
			outerRoot := graftTestTree(t, "standard", "OUTER", NewDirective("/parent"))
			innerRoot := graftTestTree(t, "standard", "INNER", NewDirective("/branch"))
			previous := graftTestTree(t, "lazy", "PREVIOUS")
			source := graftDispatchSource(t, sourceKind == "native")

			if err := innerRoot.Graft("/branch/slot", previous); err != nil {
				t.Fatal(err)
			}

			target := &graftDispatchForwardingTarget{
				graftTestForeignTree: newGraftTestForeignTree(),
				outerRoot:            outerRoot,
				innerRoot:            innerRoot,
			}

			if nativeSource, ok := source.(*tree); ok {
				target.expectedContent, target.expectedVersion = nativeSource.getWithVersion()
				target.expectedContent = bytes.Clone(target.expectedContent)
			}

			if err := outerRoot.Graft("/parent/boundary", target); err != nil {
				t.Fatal(err)
			}

			if err := outerRoot.Graft("/parent/boundary/branch/slot", source); err != nil {
				t.Fatal(err)
			}

			if !target.locksReleased || target.calls["Graft"] != "/branch/slot" || target.graftSource != source {
				t.Fatalf("custom forwarding target received path %q, source %p, locks released %v", target.calls["Graft"], target.graftSource, target.locksReleased)
			}

			branch := innerRoot.pickChild("branch").(*tree)
			if branch.pickChild("slot") != source {
				t.Fatal("forwarding into the independent native root lost source identity")
			}

			if previous.mount.Load() != nil || previous.Path() != "" {
				t.Fatal("forwarded replacement did not detach the previous native child")
			}

			if nativeSource, ok := source.(*tree); ok {
				if nativeSource.Path() != "/branch/slot" {
					t.Fatalf("forwarded native source Path = %q", nativeSource.Path())
				}

				graftTestGet(t, innerRoot, "/branch/slot/leaf", "INNER-source")
			} else if len(source.(*graftTestForeignTree).calls) != 0 {
				t.Fatal("custom-source forwarding invoked source operations")
			}
		})
	}
}

func TestGraft_NameValidationIsMountOnly(t *testing.T) {
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
		other := graftTestTree(t, "lazy", "OTHER")
		if err := tc.receiver.Graft(tc.path, other); err == nil {
			t.Errorf("Graft accepted an ambiguous name at %q", tc.path)
		}

		if other.Path() != "" {
			t.Fatal("rejected source gained an owner")
		}

		if err := tc.receiver.Set(NewDirective(tc.path, appendProc("-new"))); err != nil {
			t.Fatalf("Set rejected a path accepted by its entry parser: %v", err)
		}

		if !tc.receiver.Has(tc.path) {
			t.Fatal("Set did not preserve the entry parser's path")
		}

		graftTestGet(t, tc.receiver, tc.path, "D-new")
	}
}
