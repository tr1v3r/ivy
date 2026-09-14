package ivy

import (
	"context"
	"strings"
	"testing"

	"github.com/tr1v3r/ivy/driver"
)

// Regression test for audit finding H4: dynamicSplit used a plain ParamAware
// type assertion, so a CombinedProcessor hiding a TemplateProcessor inside was
// treated as fully static — the first request's params were realized into the
// node cache and served unchanged to every later request.
func TestCombinedProcessorParamIsolation(t *testing.T) {
	// seed content with a placeholder, interpolate it per request, then mark:
	// the chain order must be preserved inside the dynamic layer.
	seed := &driver.RawProcessor{Proc: func(_ *driver.RealizeContext, _ []byte) ([]byte, error) {
		return []byte("user=${user}"), nil
	}}
	tpl := &driver.TemplateProcessor{} // content-interpolation mode
	marker := &driver.RawProcessor{Proc: func(_ *driver.RealizeContext, before []byte) ([]byte, error) {
		return append(before, []byte("|mark")...), nil
	}}

	tree, err := NewLazyTileTree("t", "", NewDirective("/", driver.CombineProcessor(seed, tpl, marker)))
	if err != nil {
		t.Fatalf("build tree: %v", err)
	}

	first, err := tree.GetWithContext(&driver.RealizeContext{Context: context.Background(), Params: map[string]string{"user": "alice"}}, "/")
	if err != nil {
		t.Fatalf("first query: %v", err)
	}
	if got, want := string(first), "user=alice|mark"; got != want {
		t.Fatalf("first query: got %q, want %q", got, want)
	}

	second, err := tree.GetWithContext(&driver.RealizeContext{Context: context.Background(), Params: map[string]string{"user": "bob"}}, "/")
	if err != nil {
		t.Fatalf("second query: %v", err)
	}
	if got, want := string(second), "user=bob|mark"; got != want {
		t.Fatalf("second query leaked the first request's params: got %q, want %q", got, want)
	}
}

// Fully static combined chains must keep their static (cacheable) treatment.
func TestCombinedProcessorStaticStaysCached(t *testing.T) {
	realized := 0
	counter := &driver.RawProcessor{Proc: func(_ *driver.RealizeContext, before []byte) ([]byte, error) {
		realized++
		return append(before, []byte("+")...), nil
	}}

	tree, err := NewLazyTileTree("t", "", NewDirective("/", driver.CombineProcessor(counter)))
	if err != nil {
		t.Fatalf("build tree: %v", err)
	}

	for i := 0; i < 2; i++ {
		out, err := tree.GetWithContext(&driver.RealizeContext{Context: context.Background(), Params: map[string]string{"user": "alice"}}, "/")
		if err != nil {
			t.Fatalf("query %d: %v", i+1, err)
		}
		if !strings.HasSuffix(string(out), "+") {
			t.Fatalf("query %d: unexpected content %q", i+1, out)
		}
	}
	if realized != 1 {
		t.Errorf("static combined chain realized %d times, want exactly 1 (cached)", realized)
	}
}
