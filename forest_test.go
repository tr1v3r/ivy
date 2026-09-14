package ivy

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/tr1v3r/ivy/driver"
)

// testPathDriver is the driver shape used by the stress/benchmark suite.
// The parser is embedded as the CONCRETE *DelimiterPathParser (not the
// PathParser interface) so the composite implements driver.SegmentParser
// and the engine's single-parse descent is what the suite exercises.
//
// A bundled driver type (DummyDriver) must NOT be embedded alongside:
// DummyDriver forwards ParseSegments itself, so both embeddees would
// provide it at the same depth, the selector would be ambiguous, and the
// composite would silently lose SegmentParser (falling back to per-level
// parsing). Name() is provided directly instead.
type testPathDriver struct {
	driver.Modem
	*driver.DelimiterPathParser
	driver.StdRealizer
}

func (testPathDriver) Name() string { return "test" }

// newTestDriver returns a minimal driver composite for building test trees.
func newTestDriver() driver.Driver {
	return &testPathDriver{Modem: driver.DummyModem, DelimiterPathParser: driver.SlashPathParser}
}

func TestForest_Lifecycle(t *testing.T) {
	build := func() Tree {
		tree, err := NewTree(newTestDriver(), "life", `{"v":0}`,
			NewDirective("/", &driver.JSONProcessor{T: "create", JSONPath: "v", V: []byte("1")}),
		)
		if err != nil {
			t.Fatalf("build tree fail: %s", err)
		}
		return tree
	}

	f := NewForest(build)

	if f.Get("life") == nil {
		t.Fatal("expected tree life registered by NewForest")
	}
	if f.Get("missing") != nil {
		t.Error("expected nil tree for unknown name")
	}

	// GetVal happy path
	val, err := f.GetVal("life", "/")
	if err != nil {
		t.Fatalf("GetVal fail: %s", err)
	}
	if string(val) != `{"v":"1"}` {
		t.Errorf("unexpected GetVal result: %s", val)
	}

	// GetVal on missing tree
	if _, err := f.GetVal("missing", "/"); !errors.Is(err, ErrNotExistsTree) {
		t.Errorf("expected ErrNotExistsTree, got %v", err)
	}

	// GetValWithContext
	rc := &driver.RealizeContext{Context: context.Background(), Params: map[string]string{"k": "v"}}
	if _, err := f.GetValWithContext(rc, "life", "/"); err != nil {
		t.Errorf("GetValWithContext fail: %s", err)
	}

	// Refresh without interval rebuilds all trees
	f.Refresh()

	// RefreshTree rebuilds the bound builder; unknown name is a no-op
	f.RefreshTree("life")
	f.RefreshTree("missing")

	// Set nil tree is a no-op, Set real tree registers it
	f.Set(nil)
	other, err := NewTree[*directive](newTestDriver(), "other", `{"o":1}`)
	if err != nil {
		t.Fatalf("build other fail: %s", err)
	}
	f.Set(other)
	if f.Get("other") == nil {
		t.Error("expected tree other registered")
	}

	// SetDefaultContext propagates to all trees
	f.SetDefaultContext(rc)

	// Info mentions both trees
	info := f.Info()
	if !strings.Contains(info, "life") || !strings.Contains(info, "other") {
		t.Errorf("unexpected info: %s", info)
	}

	// Register + Append flows
	appended := f.Append(func() Tree {
		tree, _ := NewTree[*directive](newTestDriver(), "appended", `{}`)
		return tree
	})
	if appended == nil {
		t.Error("Append should return the forest")
	}
	f.Register(func() Tree {
		tree, _ := NewTree[*directive](newTestDriver(), "registered", `{}`)
		return tree
	})
	f.Build()
	if f.Get("appended") == nil || f.Get("registered") == nil {
		t.Error("expected appended and registered trees after Build")
	}
}

func TestForest_RegisterPanickingBuilder(t *testing.T) {
	f := NewForest()

	// Register wraps builders with panic recovery; Build must not blow up.
	f.Register(func() Tree { panic("boom") })
	f.Build()

	if f.Get("anything") != nil {
		t.Error("panicking builder should not register a tree")
	}
}

func TestForest_RateLimited(t *testing.T) {
	build := func() Tree {
		tree, err := NewLazyInstantTree(newTestDriver(), "limited", `{}`,
			NewDirective("/", &driver.JSONProcessor{T: "create", JSONPath: "ok", V: []byte("true")}),
		)
		if err != nil {
			t.Fatalf("build tree fail: %s", err)
		}
		return tree
	}

	f := NewForest(build)

	// limit 0 with burst 0 never allows a request
	f.SetRateLimit(rate.Limit(0), 0)

	if _, err := f.GetVal("limited", "/"); !errors.Is(err, ErrRateLimited) {
		t.Errorf("expected ErrRateLimited from forest limiter, got %v", err)
	}
	if _, err := f.GetValWithContext(nil, "limited", "/"); !errors.Is(err, ErrRateLimited) {
		t.Errorf("expected ErrRateLimited from forest limiter (context variant), got %v", err)
	}
}

func TestForest_BindTreeBuilder(t *testing.T) {
	f := NewForest()

	// before binding, RefreshTree is a no-op
	f.RefreshTree("bound")

	tree, err := NewTree[*directive](newTestDriver(), "bound", `{"b":1}`)
	if err != nil {
		t.Fatalf("build tree fail: %s", err)
	}
	f.Set(tree)

	// bind a builder that produces a different payload, then refresh
	f.Register(func() Tree {
		refreshed, _ := NewTree[*directive](newTestDriver(), "bound", `{"b":2}`)
		return refreshed
	})
	f.Build() // binds name -> builder
	f.RefreshTree("bound")

	val, err := f.GetVal("bound", "/")
	if err != nil {
		t.Fatalf("GetVal fail: %s", err)
	}
	if string(val) != `{"b":2}` {
		t.Errorf("expected refreshed content, got %s", val)
	}
}

func TestForest_RefreshIntervalBlocks(t *testing.T) {
	// Refresh with an interval blocks; run it in a goroutine and give it a tick.
	f := NewForest(func() Tree {
		tree, _ := NewTree[*directive](newTestDriver(), "ticker", `{}`)
		return tree
	})

	done := make(chan struct{})
	go func() { f.Refresh(time.Hour); close(done) }()

	// give the refresh goroutine a moment to start, then move on;
	// the test must not wait for it.
	time.Sleep(10 * time.Millisecond)
	select {
	case <-done:
		t.Error("Refresh(interval) should still be blocking")
	default:
	}
}
