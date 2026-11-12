package ivy

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tr1v3r/ivy/driver"
)

func TestGraftAt_ParentRefreshDuringPreparation(t *testing.T) {
	dst := graftTestTree(t, "standard", "D", NewDirective("/", appendProc("-old")))
	started, release := make(chan struct{}), make(chan struct{})
	var active atomic.Bool
	var startedOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	src := graftTestTree(t, "standard", "SOURCE", NewDirective("/", &driver.RawProcessor{
		Proc: func(rc *driver.RealizeContext, before []byte) ([]byte, error) {
			if active.Load() {
				startedOnce.Do(func() { close(started) })
				<-release
			}
			return appendProc("-s").Process(rc, before)
		},
	}))
	active.Store(true)
	done := make(chan error, 1)
	go func() { done <- GraftAt(dst, "/mount", src) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("graft preparation did not start")
	}
	// Updating an existing directive does not change topology. Publication
	// can advance while the complete replacement is prepared off-tree.
	if err := dst.Set(NewDirective("/", appendProc("-new"))); err != nil {
		t.Fatal(err)
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	graftTestGet(t, dst, "/mount", "D-new-s")
	// Set must retain the refreshed pre-processor base, not the old mount
	// snapshot or the node's previous output.
	if err := dst.Set(NewDirective("/mount", appendProc("-changed"))); err != nil {
		t.Fatal(err)
	}
	graftTestGet(t, dst, "/mount", "D-new-changed")
}

func TestGraftAt_CopyAlreadyMixedSubtree(t *testing.T) {
	src := graftTestTree(t, "lazy", "SOURCE", NewDirective("/", appendProc("-lazy")))
	standard := graftTestTree(t, "standard", "IGNORED", NewDirective("/", appendProc("-standard")))
	if err := GraftAt(src, "/branch", standard); err != nil {
		t.Fatal(err)
	}
	dst := graftTestTree(t, "standard", "D")
	if err := GraftAt(dst, "/mount", src); err != nil {
		t.Fatal(err)
	}
	instant := graftTestTree(t, "instant", "IGNORED", NewDirective("/", appendProc("-instant")))
	if err := GraftAt(dst, "/mount/branch/live", instant); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		graftTestGet(t, dst, "/mount/branch/live", "D-lazy-standard-instant")
	}
	graftTestGet(t, src, "/branch", "SOURCE-lazy-standard")
	if src.Has("/branch/live") {
		t.Fatal("editing copied mixed subtree changed source topology")
	}
}

func TestGraftAt_RejectsUnrepresentableNames(t *testing.T) {
	dst := graftTestTree(t, "standard", "D")
	src := graftTestTree(t, "lazy", "SOURCE")
	if err := GraftAt(dst, "/a//b", src); err == nil {
		t.Fatal("accepted an empty path segment")
	}
	parser := &testPathDriver{Modem: driver.DummyModem, DelimiterPathParser: new(driver.DelimiterPathParser).WithDelimiter(".")}
	withSlash, err := NewLazyTree(parser, "source", "SOURCE", NewDirective(".a/b", appendProc("-s")))
	if err != nil {
		t.Fatal(err)
	}
	if err := GraftAt(dst, "/mount", withSlash); err == nil {
		t.Fatal("accepted a child name that splits under destination parser")
	}
	if dst.Has("/mount") {
		t.Fatal("invalid source was partially published")
	}
}
