package ivy

import (
	"bytes"
	"fmt"
	"testing"
)

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

					if err := root.Graft("/parent/boundary/slot", source); err != nil {
						t.Fatal(err)
					}

					if parent.pickChild("boundary") != target {
						t.Fatal("delegation replaced the custom target instead of entering it")
					}

					if target.calls["Graft"] != "/slot" || target.graftSource != source {
						t.Fatalf("custom target received path %q and source %p; want /slot and %p", target.calls["Graft"], target.graftSource, source)
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

				previous := graftTestTree(t, "lazy", "PREVIOUS", NewDirective("/old"))
				if err := root.Graft("/parent/slot", previous); err != nil {
					t.Fatal(err)
				}

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
