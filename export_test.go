package ivy_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tr1v3r/stream"

	"github.com/tr1v3r/ivy"
	"github.com/tr1v3r/ivy/driver"
)

// newTestServer spins up a local HTTP server replying with body,
// so curl-based tests never depend on external network access.
func newTestServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestBuildTree_curl_single(t *testing.T) {
	srv := newTestServer(t, `{"msg":"pong"}`)

	tree, err := ivy.NewYAMLTree("local", "", ivy.NewDirective("/", &driver.CURLProcessor{URL: srv.URL}))
	if err != nil {
		t.Errorf("build tree fail: %s", err)
		return
	}
	rule, _ := tree.Get("")
	if string(rule) != `{"msg":"pong"}` {
		t.Errorf("unexpected curl rule: %s", rule)
	}
}

func TestBuildForest_curl_tree(t *testing.T) {
	srv := newTestServer(t, `{"msg":"pong"}`)

	f := ivy.NewForest(func() ivy.Tree {
		tree, err := ivy.NewYAMLTree("local", "", ivy.NewDirective("/", &driver.CURLProcessor{URL: srv.URL}))
		if err != nil {
			t.Errorf("build tree fail: %s", err)
			return nil
		}
		rule, _ := tree.Get("")
		t.Logf("get rule by curl: %s", rule)
		return tree
	})
	rule, _ := f.Get("local").Get("/")
	if string(rule) != `{"msg":"pong"}` {
		t.Errorf("unexpected forest rule: %s", rule)
	}
}

func TestBuildForest_stream(t *testing.T) {
	urls := []string{
		newTestServer(t, "first").URL,
		newTestServer(t, "second").URL,
	}

	trees := stream.SliceOf(urls[0], urls[1]).Parallel(64).Convert(func(url string) any {
		tree, _ := ivy.NewYAMLTree("url", "", ivy.NewDirective("/", &driver.CURLProcessor{URL: url}))
		return tree
	}).Collect(func(trees ...any) any {
		var treesArray []ivy.Tree
		for _, tree := range trees {
			treesArray = append(treesArray, tree.(ivy.Tree))
		}
		return treesArray
	}).([]ivy.Tree)

	// Parallel conversion does not guarantee output order, so match by content.
	got := make(map[string]bool)
	for i, tree := range trees {
		rule, err := tree.Get("")
		if err != nil {
			t.Errorf("get tree [%d] fail: %s", i, err)
			continue
		}
		got[string(rule)] = true
		t.Logf("got tree [%d] %s: %s", i, tree.Name(), rule)
	}
	for _, want := range []string{"first", "second"} {
		if !got[want] {
			t.Errorf("missing tree content %q, got %v", want, got)
		}
	}
}

func TestTileTree(t *testing.T) {
	var proc = func(content string) func(*driver.RealizeContext, []byte) ([]byte, error) {
		return func(_ *driver.RealizeContext, _ []byte) ([]byte, error) {
			return []byte(content), nil
		}
	}

	tree, err := ivy.NewLazyTileTree("test_tile_tree", "template",
		ivy.NewDirective("/abc", &driver.RawProcessor{Proc: proc("content1")}),
		ivy.NewDirective("/123", &driver.RawProcessor{Proc: proc("content2")}),
		ivy.NewDirective("/test", &driver.RawProcessor{Proc: proc("content3")}),
		ivy.NewDirective("/@@@", &driver.RawProcessor{Proc: proc("content4")}),
	)
	if err != nil {
		t.Fatalf("build tile tree fail: %s", err)
	}

	var expected = map[string]string{
		"/abc":  "content1",
		"/123":  "content2",
		"/test": "content3",
		"/@@@":  "content4",
	}
	for path, want := range expected {
		data, err := tree.Get(path)
		if err != nil {
			t.Errorf("get %s fail: %s", path, err)
			continue
		}
		if string(data) != want {
			t.Errorf("get %s: expect %q, got %q", path, want, data)
		}
	}
}
