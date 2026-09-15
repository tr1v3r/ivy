package ivy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tr1v3r/ivy/driver"
)

func TestBuildTree(t *testing.T) {
	var testcases = []struct {
		Rules    []*directive
		Expected string
	}{
		{Rules: []*directive{
			{path: "/a/b/c/d"},
			{path: "/a/b/c"},
			{path: "/"},
			{path: "x/y/z"},
			{path: "/a/b/m"},
		},
			Expected: `{"a":{"b":{"c":{"d":{}},"m":{}}},"x":{"y":{"z":{}}}}`},
	}

	for index, item := range testcases {
		tree, err := NewTree(&struct {
			driver.Modem
			driver.PathParser
			driver.StdRealizer
			driver.DummyDriver // just provide a method Name
		}{Modem: driver.DummyModem, PathParser: driver.SlashPathParser},
			"unit_test_"+fmt.Sprint(index), "{}", item.Rules...)
		if err != nil {
			t.Errorf("build tree fail: %s", err)
			return
		}

		if result := string(tree.ShowStruct()); item.Expected != result {
			t.Errorf("build tree fail, expect: %s\ngot: %s", item.Expected, result)

			printS, _ := json.MarshalIndent(tree.ShowStruct(), "", "\t")
			t.Logf("got tree struct: %s", printS)
		}
	}
}

func TestForest_run(t *testing.T) {
	var testcases = []struct {
		Name       string
		TargetPath string
		Expected   string
	}{
		{"json_tree_1", "a/b", `{"id":1,"author":{"first":"river"},"name":"root","info":{"path":"path:a/b"}}`},
		{"json_tree_1", "a/b/c/d", `{"id":1,"author":{"first":"river"},"name":"root","info":{"path":"path:a/b/c/d"}}`},
		{"json_tree_1", "a/b/x", `{"id":1,"author":{"first":"river"},"name":"root","info":{"path":"path:a/b"}}`},
		{"json_tree_2", "x/y/z", `{"code":200,"msg":"pong"}`},
	}

	f := InitForest(t)
	t.Logf("forest info: %s", f.Info())

	for _, item := range testcases {
		rule, err := f.Get(item.Name).Get(item.TargetPath)
		if err != nil {
			t.Errorf("query rule %s on %s fail: %s", item.TargetPath, item.Name, err)
			continue
		}
		if string(rule) != item.Expected {
			t.Errorf("check rule fail, expect: %s\ngot: %s", item.Expected, string(rule))
			continue
		}
		t.Logf("got rule: %s", string(rule))
	}
}

func InitForest(t *testing.T) Forest {
	// local pong server so the curl directive works without network access
	pong := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"msg":"pong"}`))
	}))
	t.Cleanup(pong.Close)

	var builders = []TreeBuilder{
		func() Tree {
			var rules = []*directive{
				{path: "/", processors: []driver.Processor{
					&driver.JSONProcessor{T: "create", JSONPath: "author.first", V: []byte("river")},
					&driver.JSONProcessor{T: "create", JSONPath: "name", V: []byte("root")},
				}},
				{path: "/a/b/c/d", processors: []driver.Processor{
					&driver.JSONProcessor{T: "create", JSONPath: "info.path", V: []byte("path:a/b/c/d")},
				}},
				{path: "/a/b/c", processors: []driver.Processor{
					&driver.JSONProcessor{T: "create", JSONPath: "info.path", V: []byte("path:a/b/c")},
				}},
				{path: "/a/b", processors: []driver.Processor{
					&driver.JSONProcessor{T: "create", JSONPath: "info.path", V: []byte("path:a/b")},
				}},
				{path: "/x/y/z", processors: []driver.Processor{
					&driver.JSONProcessor{T: "create", JSONPath: "info.path", V: []byte("path:x/y/z")},
				}},
				{path: "/a/b/m", processors: []driver.Processor{
					&driver.JSONProcessor{T: "create", JSONPath: "info.path", V: []byte("path:a/b/m")},
				}},
			}
			tree, err := NewTree(&struct {
				driver.Modem
				driver.PathParser
				driver.StdRealizer
				driver.DummyDriver // just provide a method Name
			}{Modem: driver.DummyModem, PathParser: driver.SlashPathParser},
				"json_tree_1", `{"id":1}`, rules...)
			if err != nil {
				t.Errorf("build tree fail: %s", err)
				return nil
			}
			return tree
		},
		func() Tree {
			var rules = []*directive{
				{path: "/a/b/c/d", processors: nil},
				{path: "/a/b/c", processors: nil},
				{path: "/", processors: []driver.Processor{&driver.CURLProcessor{URL: pong.URL + "/ping"}}},
				{path: "/x/y/z", processors: nil},
				{path: "/a/b/m", processors: nil},
			}
			tree, err := NewTree(&struct {
				driver.Modem
				driver.PathParser
				driver.StdRealizer
				driver.DummyDriver // just provide a method Name
			}{Modem: driver.DummyModem, PathParser: driver.SlashPathParser},
				"json_tree_2", `{"id":2}`, rules...)
			if err != nil {
				t.Errorf("build tree fail: %s", err)
				return nil
			}
			return tree
		},
	}
	return NewForest(builders...)
}

func TestLazyCacheTree_TTLExpiry(t *testing.T) {
	var realizeCount int32

	// Use a processor that produces different output each time to detect re-realization
	incrementProcessor := &driver.RawProcessor{
		Proc: func(_ *driver.RealizeContext, before []byte) ([]byte, error) {
			n := atomic.AddInt32(&realizeCount, 1)
			return fmt.Appendf(nil, `{"realize":%d}`, n), nil
		},
	}

	tree, err := NewLazyCacheTree(
		&struct {
			driver.Modem
			driver.PathParser
			driver.StdRealizer
			driver.DummyDriver
		}{Modem: driver.DummyModem, PathParser: driver.SlashPathParser},
		"cache_test", `{}`, 50*time.Millisecond,
		NewDirective("/", incrementProcessor),
	)
	if err != nil {
		t.Fatalf("build tree fail: %s", err)
	}

	// First Get — triggers realization
	result1, err := tree.Get("/")
	if err != nil {
		t.Fatalf("first get fail: %s", err)
	}
	if atomic.LoadInt32(&realizeCount) != 1 {
		t.Fatalf("expected realizeCount=1, got %d", realizeCount)
	}

	// Second Get immediately — should use cache
	result2, err := tree.Get("/")
	if err != nil {
		t.Fatalf("second get fail: %s", err)
	}
	if string(result1) != string(result2) {
		t.Errorf("cache miss on immediate second get: %s vs %s", result1, result2)
	}
	if atomic.LoadInt32(&realizeCount) != 1 {
		t.Errorf("expected realizeCount=1 (cached), got %d", realizeCount)
	}

	// Wait for TTL to expire
	time.Sleep(60 * time.Millisecond)

	// Third Get — should re-realize with different result
	result3, err := tree.Get("/")
	if err != nil {
		t.Fatalf("third get fail: %s", err)
	}
	if atomic.LoadInt32(&realizeCount) != 2 {
		t.Errorf("expected realizeCount=2 (re-realized), got %d", realizeCount)
	}
	if string(result3) == string(result1) {
		t.Errorf("expected different result after TTL expiry")
	}
	t.Logf("cached: %s, after TTL: %s", result1, result3)
}

func TestLazyCacheTree_ZeroTTL(t *testing.T) {
	// Zero TTL should behave like standard lazy — cache forever
	tree, err := NewLazyCacheTree(
		&struct {
			driver.Modem
			driver.PathParser
			driver.StdRealizer
			driver.DummyDriver
		}{Modem: driver.DummyModem, PathParser: driver.SlashPathParser},
		"zero_ttl_test", `{"v":0}`, 0,
		NewDirective("/", &driver.JSONProcessor{T: "create", JSONPath: "v", V: []byte("1")}),
	)
	if err != nil {
		t.Fatalf("build tree fail: %s", err)
	}

	result1, _ := tree.Get("/")
	time.Sleep(20 * time.Millisecond)
	result2, _ := tree.Get("/")
	if string(result1) != string(result2) {
		t.Errorf("zero TTL should cache forever, got different results: %s vs %s", result1, result2)
	}
}

func TestRealizeWithContext_Dispatch(t *testing.T) {
	var gotRC *driver.RealizeContext

	proc := &driver.RawProcessor{
		Proc: func(_ *driver.RealizeContext, before []byte) ([]byte, error) {
			// keep the document valid JSON for the JSONProcessor
			// below (a non-JSON base is now an explicit error, #57)
			return append(before[:len(before)-1], []byte(`"ctx":true}`)...), nil
		},
	}

	plainProc := &driver.JSONProcessor{T: "create", JSONPath: "key", V: []byte("val")}

	r := new(driver.StdRealizer)
	rc := &driver.RealizeContext{Params: map[string]string{"foo": "bar"}}
	result, err := r.Realize(rc, []byte("{}"), proc, plainProc)
	if err != nil {
		t.Fatalf("Realize fail: %s", err)
	}

	_ = gotRC
	t.Logf("got result: %s", result)
}

func TestTree_GetWithContext(t *testing.T) {
	var gotRC *driver.RealizeContext

	proc := &contextCapturingProcessor{capture: func(rc *driver.RealizeContext) { gotRC = rc }}

	tree, err := NewLazyTree(
		&struct {
			driver.Modem
			driver.PathParser
			driver.StdRealizer
			driver.DummyDriver
		}{Modem: driver.DummyModem, PathParser: driver.SlashPathParser},
		"ctx_test", `{}`,
		NewDirective("/a/b", proc),
	)
	if err != nil {
		t.Fatalf("build tree fail: %s", err)
	}

	rc := driver.RealizeContext{Params: map[string]string{"user": "alice"}}
	result, err := tree.GetWithContext(&rc, "/a/b")
	if err != nil {
		t.Fatalf("GetWithContext fail: %s", err)
	}

	if gotRC == nil || gotRC.Params["user"] != "alice" {
		t.Errorf("expected user=alice, got %v", gotRC)
	}
	if gotRC.TreePath != "/a/b" {
		t.Errorf("expected TreePath=/a/b, got %s", gotRC.TreePath)
	}

	t.Logf("got result: %s", result)
}

// contextCapturingProcessor is a test processor that captures the RealizeContext
type contextCapturingProcessor struct {
	capture func(*driver.RealizeContext)
}

func (p *contextCapturingProcessor) Type() string         { return "test" }
func (p *contextCapturingProcessor) Path() string         { return "" }
func (p *contextCapturingProcessor) Author() string       { return "" }
func (p *contextCapturingProcessor) CreatedAt() time.Time { return time.Time{} }
func (p *contextCapturingProcessor) Load([]byte) error    { return nil }
func (p *contextCapturingProcessor) Save() []byte         { return nil }
func (p *contextCapturingProcessor) Process(rc *driver.RealizeContext, before []byte) ([]byte, error) {
	if p.capture != nil {
		p.capture(rc)
	}
	return []byte(`{"ok":true}`), nil
}

func TestRawProcessor_Fallback(t *testing.T) {
	proc := &driver.RawProcessor{
		Proc: func(_ *driver.RealizeContext, before []byte) ([]byte, error) {
			return []byte("fallback"), nil
		},
	}

	result, err := proc.Process(nil, []byte("input"))
	if err != nil {
		t.Fatalf("Process fail: %s", err)
	}
	if string(result) != "fallback" {
		t.Errorf("expected fallback, got %s", result)
	}
}

func TestTree_Fallback(t *testing.T) {
	var fallbackCalled bool

	tree, err := NewTree(
		&struct {
			driver.Modem
			driver.PathParser
			driver.StdRealizer
			driver.DummyDriver
		}{Modem: driver.DummyModem, PathParser: driver.SlashPathParser},
		"fallback_test", `{"base":true}`,
		NewDirective("/", &driver.JSONProcessor{T: "create", JSONPath: "root", V: []byte("yes")}),
	)
	if err != nil {
		t.Fatalf("build tree fail: %s", err)
	}

	tree.SetFallback(&driver.RawProcessor{
		Proc: func(_ *driver.RealizeContext, before []byte) ([]byte, error) {
			fallbackCalled = true
			return append(before, []byte(`,"fallback":true}`)...), nil
		},
	})

	// Query a path that does not exist — should trigger fallback
	result, err := tree.Get("/nonexistent")
	if err != nil {
		t.Fatalf("get fail: %s", err)
	}
	if !fallbackCalled {
		t.Error("expected fallback to be called")
	}
	s := string(result)
	if !containsJSON(s, `"fallback":true`) {
		t.Errorf("expected fallback content in result, got: %s", s)
	}
	t.Logf("fallback result: %s", s)

	// Query existing root path — should NOT trigger fallback (target found at leaf)
	fallbackCalled = false
	result2, err := tree.Get("/")
	if err != nil {
		t.Fatalf("get root fail: %s", err)
	}
	if fallbackCalled {
		t.Error("fallback should not be called for existing root path")
	}
	t.Logf("root result (no fallback): %s", string(result2))
}

func TestTree_FallbackWithContext(t *testing.T) {
	var gotRC *driver.RealizeContext

	tree, err := NewTree(
		&struct {
			driver.Modem
			driver.PathParser
			driver.StdRealizer
			driver.DummyDriver
		}{Modem: driver.DummyModem, PathParser: driver.SlashPathParser},
		"ctx_fallback_test", `{}`,
		NewDirective("/", &driver.JSONProcessor{T: "create", JSONPath: "v", V: []byte("1")}),
	)
	if err != nil {
		t.Fatalf("build tree fail: %s", err)
	}

	tree.SetFallback(&contextCapturingProcessor{capture: func(rc *driver.RealizeContext) {
		gotRC = rc
	}})

	rc := &driver.RealizeContext{Params: map[string]string{"user": "bob"}}
	_, err = tree.GetWithContext(rc, "/missing")
	if err != nil {
		t.Fatalf("GetWithContext fail: %s", err)
	}
	if gotRC == nil || gotRC.Params["user"] != "bob" {
		t.Errorf("expected user=bob in fallback context, got %v", gotRC)
	}
}

func TestCombineProcessor(t *testing.T) {
	combined := driver.CombineProcessor(
		&driver.JSONProcessor{T: "create", JSONPath: "name", V: []byte("alice")},
		&driver.JSONProcessor{T: "create", JSONPath: "age", V: []byte("30")},
	)

	result, err := combined.Process(nil, []byte(`{}`))
	if err != nil {
		t.Fatalf("Process fail: %s", err)
	}
	s := string(result)
	if !strings.Contains(s, `"name":"alice"`) {
		t.Errorf("expected name=alice, got: %s", s)
	}
	if !strings.Contains(s, `"age":"30"`) {
		t.Errorf("expected age=30, got: %s", s)
	}
	t.Logf("combined result: %s", s)
}

func TestCombineProcessor_WithFallback(t *testing.T) {
	// Use CombineProcessor as a tree fallback
	combined := driver.CombineProcessor(
		&driver.JSONProcessor{T: "create", JSONPath: "fallback", V: []byte("true")},
		&driver.JSONProcessor{T: "create", JSONPath: "extra", V: []byte("data")},
	)

	tree, err := NewTree[*directive](
		&struct {
			driver.Modem
			driver.PathParser
			driver.StdRealizer
			driver.DummyDriver
		}{Modem: driver.DummyModem, PathParser: driver.SlashPathParser},
		"combined_fallback_test", `{"base":1}`,
	)
	if err != nil {
		t.Fatalf("build tree fail: %s", err)
	}

	tree.SetFallback(combined)

	result, err := tree.Get("/missing")
	if err != nil {
		t.Fatalf("get fail: %s", err)
	}
	s := string(result)
	if !strings.Contains(s, `"fallback":"true"`) {
		t.Errorf("expected fallback=true, got: %s", s)
	}
	if !strings.Contains(s, `"extra":"data"`) {
		t.Errorf("expected extra=data, got: %s", s)
	}
	t.Logf("combined fallback result: %s", s)
}

func containsJSON(s, sub string) bool {
	return strings.Contains(s, sub)
}
