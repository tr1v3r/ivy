package web

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/tr1v3r/ivy"
	"github.com/tr1v3r/ivy/driver"
)

func init() { gin.SetMode(gin.TestMode) }

func newTestRouter() *gin.Engine {
	r := gin.New()
	RegisterAPI(r.Group("api/v1"))
	return r
}

func TestPing(t *testing.T) {
	r := newTestRouter()

	for _, path := range []string{"/api/v1/ping", "/api/v1/rule/ping"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK {
			t.Errorf("GET %s: expect 200, got %d", path, w.Code)
		}
		if !strings.Contains(w.Body.String(), "pong") {
			t.Errorf("GET %s: expect pong, got %s", path, w.Body.String())
		}
	}
}

func TestGetRule(t *testing.T) {
	// a processor that interpolates request params proves the RealizeContext plumbing
	paramProc := &driver.RawProcessor{Proc: func(rc *driver.RealizeContext, before []byte) ([]byte, error) {
		who := rc.Params["who"]
		if who == "" {
			who = "stranger"
		}
		return []byte(`{"hello":"` + who + `"}`), nil
	}}

	// instant tree: re-realizes per request, so query params reach the processor
	InitForest(func() ivy.Tree {
		tree, err := ivy.NewLazyInstantTree(driver.NewJSONDriver(), treeName, `{}`,
			ivy.NewDirective("/", paramProc))
		if err != nil {
			t.Fatalf("build tree fail: %s", err)
		}
		return tree
	})
	RefreshForest() // rebuild once for good measure

	r := newTestRouter()

	var testcases = []struct {
		Name   string
		Query  string
		Expect string
	}{
		{"with param", "name=default&path=/&who=world", "world"},
		{"fallback param", "name=default&path=/", "stranger"},
	}
	for _, item := range testcases {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/rule?"+item.Query, nil))

		if w.Code != http.StatusOK {
			t.Errorf("%s: expect 200, got %d (body: %s)", item.Name, w.Code, w.Body.String())
			continue
		}

		// the handler returns the raw rule bytes wrapped as a JSON value ([]byte -> base64)
		var payload any
		if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
			t.Errorf("%s: invalid json response: %s", item.Name, w.Body.String())
			continue
		}
		encoded, _ := payload.(string)
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			t.Errorf("%s: response is not base64 rule bytes: %s", item.Name, w.Body.String())
			continue
		}
		if !strings.Contains(string(decoded), item.Expect) {
			t.Errorf("%s: expect %q in rule, got %s", item.Name, item.Expect, decoded)
		}
	}
}

func TestGetRule_ErrorBranch(t *testing.T) {
	boom := errors.New("boom")
	InitForest(func() ivy.Tree {
		tree, err := ivy.NewLazyInstantTree(driver.NewJSONDriver(), treeName, `{}`,
			ivy.NewDirective("/", &driver.RawProcessor{Proc: func(_ *driver.RealizeContext, _ []byte) ([]byte, error) {
				return nil, boom
			}}))
		if err != nil {
			t.Fatalf("build tree fail: %s", err)
		}
		return tree
	})

	r := newTestRouter()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/rule?name=default&path=/", nil))

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expect 500 on realize failure, got %d (body: %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "fail") {
		t.Errorf("expected error payload, got %s", w.Body.String())
	}
}

func TestGetRule_MissingTree(t *testing.T) {
	// W1: a missing or unknown ?name= used to panic GetRule (nil Tree
	// interface method call). newTestRouter uses gin.New() without the
	// Recovery middleware, so a regression panic crashes the test binary
	// instead of being masked as a 500.
	InitForest(func() ivy.Tree {
		tree, err := ivy.NewLazyInstantTree[ivy.Directive](driver.NewJSONDriver(), treeName, `{}`)
		if err != nil {
			t.Fatalf("build tree fail: %s", err)
		}
		return tree
	})

	r := newTestRouter()

	var testcases = []struct {
		Name  string
		Query string
	}{
		{"no name", "path=/"},
		{"empty name", "name=&path=/"},
		{"unknown name", "name=missing&path=/"},
	}
	for _, item := range testcases {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/rule?"+item.Query, nil))

		if w.Code != http.StatusNotFound {
			t.Errorf("%s: expect 404, got %d (body: %s)", item.Name, w.Code, w.Body.String())
			continue
		}
		if !strings.Contains(w.Body.String(), "not found") {
			t.Errorf("%s: expected not-found payload, got %s", item.Name, w.Body.String())
		}
	}
}

// TestRefreshForest_ConcurrentWithRequests guards the W2 data race:
// cmd/serve's ticker goroutine calls RefreshForest every 5s while every
// request reads the forest in GetRule. RefreshForest used to reassign the
// global `f`, which -race reported as write web/server.go vs read
// web/handler.go. Run with `go test -race` for the discriminator; without
// -race it still exercises the concurrent path.
func TestRefreshForest_ConcurrentWithRequests(t *testing.T) {
	InitForest(DefaultBuilder())

	r := newTestRouter()

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// cmd/serve's refresh ticker equivalent
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				RefreshForest()
			}
		}
	}()

	// request load
	var reqWg sync.WaitGroup
	for i := 0; i < 4; i++ {
		reqWg.Add(1)
		go func() {
			defer reqWg.Done()
			for j := 0; j < 50; j++ {
				w := httptest.NewRecorder()
				r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/rule?name=default&path=/", nil))
				if w.Code != http.StatusOK {
					t.Errorf("unexpected status %d (body: %s)", w.Code, w.Body.String())
					return
				}
			}
		}()
	}

	reqWg.Wait()
	close(stop)
	wg.Wait()
}

func TestWebDriverName(t *testing.T) {
	if name := (webDriver{}).Name(); name != "default" {
		t.Errorf("unexpected web driver name: %s", name)
	}
}

func TestDefaultBuilder(t *testing.T) {
	build := DefaultBuilder(ivy.NewDirective("/", &driver.JSONProcessor{T: "create", JSONPath: "engine", V: []byte("ivy")}))

	tree := build()
	if tree == nil {
		t.Fatal("expected tree from default builder")
	}
	if tree.Name() != treeName {
		t.Errorf("expect tree name %q, got %q", treeName, tree.Name())
	}

	val, err := tree.Get("/")
	if err != nil {
		t.Fatalf("get fail: %s", err)
	}
	if !strings.Contains(string(val), "ivy") {
		t.Errorf("unexpected rule: %s", val)
	}
}
