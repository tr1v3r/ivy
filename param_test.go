package ivy

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tr1v3r/ivy/driver"
)

// paramRC builds a RealizeContext with the given params.
func paramRC(params map[string]string) *driver.RealizeContext {
	return &driver.RealizeContext{Context: context.Background(), Params: params}
}

// TestParamAware_CacheNotPolluted is the core regression test: querying a
// lazy tree with different params must return differentiated results while
// the node cache keeps holding the param-free static base.
func TestParamAware_CacheNotPolluted(t *testing.T) {
	tr, err := NewLazyTree(newTestDriver(), "param_cache", `{"greeting": "hello ${user}"}`,
		NewDirective("/", &driver.TemplateProcessor{}),
	)
	if err != nil {
		t.Fatalf("build tree fail: %s", err)
	}

	// query as alice
	// query as alice
	alice, err := tr.GetWithContext(paramRC(map[string]string{"user": "alice"}), "/")
	if err != nil {
		t.Fatalf("get fail: %s", err)
	}
	if !strings.Contains(string(alice), "hello alice") {
		t.Fatalf("expected alice interpolation, got: %s", alice)
	}

	// query as bob: must NOT see alice's cached result
	bob, err := tr.GetWithContext(paramRC(map[string]string{"user": "bob"}), "/")
	if err != nil {
		t.Fatalf("get fail: %s", err)
	}
	if !strings.Contains(string(bob), "hello bob") {
		t.Fatalf("expected bob interpolation, got: %s", bob)
	}

	// the node cache must still hold the param-free base
	node := tr.(*tree)
	internal := node.get()
	if strings.Contains(string(internal), "alice") || strings.Contains(string(internal), "bob") {
		t.Errorf("cache polluted with request param: %s", internal)
	}
	if !strings.Contains(string(internal), "${user}") {
		t.Errorf("expected placeholder intact in cached base, got: %s", internal)
	}

	// repeat alice query — still correct after bob's query
	again, err := tr.GetWithContext(paramRC(map[string]string{"user": "alice"}), "/")
	if err != nil {
		t.Fatalf("get fail: %s", err)
	}
	if !strings.Contains(string(again), "hello alice") {
		t.Errorf("expected alice again, got: %s", again)
	}
}

// TestParamAware_StaticPrefixCached proves the static part realizes once
// while the dynamic layer re-runs per query.
func TestParamAware_StaticPrefixCached(t *testing.T) {
	var staticRuns int32
	static := &driver.RawProcessor{Proc: func(_ *driver.RealizeContext, before []byte) ([]byte, error) {
		atomic.AddInt32(&staticRuns, 1)
		return before, nil
	}}

	tree, err := NewLazyTree(newTestDriver(), "param_split", `{"v": "${n}"}`,
		NewDirective("/", static, &driver.TemplateProcessor{}),
	)
	if err != nil {
		t.Fatalf("build tree fail: %s", err)
	}

	for _, n := range []string{"1", "2", "3", "4", "5"} {
		out, err := tree.GetWithContext(paramRC(map[string]string{"n": n}), "/")
		if err != nil {
			t.Fatalf("get %s fail: %s", n, err)
		}
		if !strings.Contains(string(out), `"${n}"`) == false && !strings.Contains(string(out), n) {
			// content is JSON-encoded: placeholder inside quotes gets replaced in place
			t.Errorf("expected n=%s in result, got: %s", n, out)
		}
	}

	if got := atomic.LoadInt32(&staticRuns); got != 1 {
		t.Errorf("static processor should realize exactly once (cached), ran %d times", got)
	}
}

// TestParamAware_GetReturnsStaticBase: the param-less Get returns the
// cached static base with placeholders intact, never a stale rendering.
func TestParamAware_GetReturnsStaticBase(t *testing.T) {
	tree, err := NewLazyTree(newTestDriver(), "param_get", `{"msg": "hi ${who}"}`,
		NewDirective("/", &driver.TemplateProcessor{}),
	)
	if err != nil {
		t.Fatalf("build tree fail: %s", err)
	}

	// dynamic query first
	if _, err := tree.GetWithContext(paramRC(map[string]string{"who": "bob"}), "/"); err != nil {
		t.Fatalf("get with context fail: %s", err)
	}

	// plain Get returns the static base, unaffected by the query above
	base, err := tree.Get("/")
	if err != nil {
		t.Fatalf("get fail: %s", err)
	}
	if !strings.Contains(string(base), "${who}") {
		t.Errorf("expected placeholder in static base, got: %s", base)
	}
	if strings.Contains(string(base), "bob") {
		t.Errorf("static base polluted by previous query: %s", base)
	}
}

// TestParamAware_InheritanceComposition: a placeholder declared at the
// root template is rendered when querying a deeper lazy node, because the
// dynamic layer applies to the inherited content at the target.
func TestParamAware_InheritanceComposition(t *testing.T) {
	tree, err := NewLazyTree(newTestDriver(), "param_inherit", `{"user": "${user}", "fixed": 1}`,
		NewDirective("/a/b",
			&driver.JSONProcessor{T: "create", JSONPath: "leaf", V: []byte("true")},
			&driver.TemplateProcessor{},
		),
	)
	if err != nil {
		t.Fatalf("build tree fail: %s", err)
	}

	out, err := tree.GetWithContext(paramRC(map[string]string{"user": "carol"}), "/a/b")
	if err != nil {
		t.Fatalf("get fail: %s", err)
	}
	s := string(out)
	if !strings.Contains(s, `"user": "carol"`) {
		t.Errorf("expected inherited placeholder rendered, got: %s", s)
	}
	if !strings.Contains(s, `"leaf":"true"`) {
		t.Errorf("expected static processor result preserved, got: %s", s)
	}

	// a different user gets a different rendering of the same node
	out2, _ := tree.GetWithContext(paramRC(map[string]string{"user": "dave"}), "/a/b")
	if !strings.Contains(string(out2), `"user": "dave"`) {
		t.Errorf("expected dave rendering, got: %s", out2)
	}
}

// TestParamAware_IntermediateDynamicNotApplied: dynamic processors on
// intermediate nodes of the queried path are documented as NOT applied;
// descent uses static content.
func TestParamAware_IntermediateDynamicNotApplied(t *testing.T) {
	tree, err := NewLazyTree(newTestDriver(), "param_mid", `{"who": "${who}"}`,
		NewDirective("/a", &driver.TemplateProcessor{Pattern: "intermediate-${who}"}),
		NewDirective("/a/b", &driver.JSONProcessor{T: "create", JSONPath: "leaf", V: []byte("1")}),
	)
	if err != nil {
		t.Fatalf("build tree fail: %s", err)
	}

	// querying /a itself DOES apply its dynamic layer
	out, err := tree.GetWithContext(paramRC(map[string]string{"who": "x"}), "/a")
	if err != nil {
		t.Fatalf("get /a fail: %s", err)
	}
	if string(out) != "intermediate-x" {
		t.Errorf("expected pattern rendering at /a, got: %s", out)
	}

	// querying /a/b passes THROUGH /a: intermediate dynamic not applied,
	// /a/b gets the static inherited base + its own static procs
	out2, err := tree.GetWithContext(paramRC(map[string]string{"who": "x"}), "/a/b")
	if err != nil {
		t.Fatalf("get /a/b fail: %s", err)
	}
	if !strings.Contains(string(out2), `"leaf":"1"`) {
		t.Errorf("expected leaf content, got: %s", out2)
	}
}

// TestParamAware_StandardTree: param-aware procs work on standard-mode
// trees too — the static base is realized at build, dynamic per query.
func TestParamAware_StandardTree(t *testing.T) {
	tree, err := NewTree(newTestDriver(), "param_std", `{"n": "${n}"}`,
		NewDirective("/", &driver.TemplateProcessor{}),
	)
	if err != nil {
		t.Fatalf("build tree fail: %s", err)
	}

	out, err := tree.GetWithContext(paramRC(map[string]string{"n": "9"}), "/")
	if err != nil {
		t.Fatalf("get fail: %s", err)
	}
	if !strings.Contains(string(out), `"n": "9"`) {
		t.Errorf("expected rendered n, got: %s", out)
	}

	out2, _ := tree.GetWithContext(paramRC(map[string]string{"n": "8"}), "/")
	if !strings.Contains(string(out2), `"n": "8"`) {
		t.Errorf("expected rendered n=8, got: %s", out2)
	}
}

// TestParamAware_FallbackParamAware: a param-aware fallback processor is
// naturally safe — fallback results were never cached.
func TestParamAware_FallbackParamAware(t *testing.T) {
	tree, err := NewTree[*directive](newTestDriver(), "param_fb", `{}`)
	if err != nil {
		t.Fatalf("build tree fail: %s", err)
	}
	tree.SetFallback(&driver.TemplateProcessor{Pattern: "fallback:${name}"})

	out, err := tree.GetWithContext(paramRC(map[string]string{"name": "eve"}), "/missing")
	if err != nil {
		t.Fatalf("get fail: %s", err)
	}
	if string(out) != "fallback:eve" {
		t.Errorf("expected rendered fallback, got: %s", out)
	}

	out2, _ := tree.GetWithContext(paramRC(map[string]string{"name": "mal"}), "/missing")
	if string(out2) != "fallback:mal" {
		t.Errorf("expected rendered fallback for mal, got: %s", out2)
	}
}
