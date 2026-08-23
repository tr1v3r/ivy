package ivy

import (
	"errors"
	"testing"
	"time"

	"github.com/tr1v3r/ivy/driver"
)

// replaceProc returns a processor whose output replaces the node content.
func replaceProc(content string) driver.Processor {
	return &driver.RawProcessor{Proc: func(_ *driver.RealizeContext, _ []byte) ([]byte, error) {
		return []byte(content), nil
	}}
}

func TestTreeConstructors(t *testing.T) {
	jsonRoot := func() driver.Processor {
		return &driver.JSONProcessor{T: "create", JSONPath: "engine", V: []byte("ivy")}
	}

	var testcases = []struct {
		Name   string
		Build  func() (Tree, error)
		Expect string
		Lazy   bool
		TTL    time.Duration // meaningful only for cache trees
	}{
		{
			Name: "json standard",
			Build: func() (Tree, error) {
				return NewJSONTree("t", `{"v":1}`, NewDirective("/", jsonRoot()))
			},
			Expect: `{"v":1,"engine":"ivy"}`,
		},
		{
			Name: "json lazy",
			Build: func() (Tree, error) {
				return NewLazyJSONTree("t", `{"v":1}`, NewDirective("/", jsonRoot()))
			},
			Expect: `{"v":1,"engine":"ivy"}`,
			Lazy:   true,
		},
		{
			Name: "json lazy instant",
			Build: func() (Tree, error) {
				return NewLazyInstantJSONTree("t", `{"v":1}`, NewDirective("/", jsonRoot()))
			},
			Expect: `{"v":1,"engine":"ivy"}`,
			Lazy:   true,
		},
		{
			Name: "json lazy cache",
			Build: func() (Tree, error) {
				return NewLazyCacheJSONTree("t", `{"v":1}`, time.Minute, NewDirective("/", jsonRoot()))
			},
			Expect: `{"v":1,"engine":"ivy"}`,
			Lazy:   true,
			TTL:    time.Minute,
		},
		{
			Name: "yaml standard",
			Build: func() (Tree, error) {
				return NewYAMLTree("t", "a: 1\n", NewDirective("/", replaceProc("b: 2\n")))
			},
			Expect: "b: 2\n",
		},
		{
			Name: "yaml lazy",
			Build: func() (Tree, error) {
				return NewLazyYAMLTree("t", "a: 1\n", NewDirective("/", replaceProc("b: 2\n")))
			},
			Expect: "b: 2\n",
			Lazy:   true,
		},
		{
			Name: "yaml lazy instant",
			Build: func() (Tree, error) {
				return NewLazyInstantYAMLTree("t", "a: 1\n", NewDirective("/", replaceProc("b: 2\n")))
			},
			Expect: "b: 2\n",
			Lazy:   true,
		},
		{
			Name: "yaml lazy cache",
			Build: func() (Tree, error) {
				return NewLazyCacheYAMLTree("t", "a: 1\n", time.Minute, NewDirective("/", replaceProc("b: 2\n")))
			},
			Expect: "b: 2\n",
			Lazy:   true,
			TTL:    time.Minute,
		},
		{
			Name: "xml standard",
			Build: func() (Tree, error) {
				return NewXMLTree("t", "<a/>", NewDirective("/", replaceProc("<b/>")))
			},
			Expect: "<b/>",
		},
		{
			Name: "xml lazy",
			Build: func() (Tree, error) {
				return NewLazyXMLTree("t", "<a/>", NewDirective("/", replaceProc("<b/>")))
			},
			Expect: "<b/>",
			Lazy:   true,
		},
		{
			Name: "xml lazy instant",
			Build: func() (Tree, error) {
				return NewLazyInstantXMLTree("t", "<a/>", NewDirective("/", replaceProc("<b/>")))
			},
			Expect: "<b/>",
			Lazy:   true,
		},
		{
			Name: "xml lazy cache",
			Build: func() (Tree, error) {
				return NewLazyCacheXMLTree("t", "<a/>", time.Minute, NewDirective("/", replaceProc("<b/>")))
			},
			Expect: "<b/>",
			Lazy:   true,
			TTL:    time.Minute,
		},
		{
			Name: "toml standard",
			Build: func() (Tree, error) {
				return NewTOMLTree("t", "a = 1\n", NewDirective("/", replaceProc("b = 2\n")))
			},
			Expect: "b = 2\n",
		},
		{
			Name: "toml lazy",
			Build: func() (Tree, error) {
				return NewLazyTOMLTree("t", "a = 1\n", NewDirective("/", replaceProc("b = 2\n")))
			},
			Expect: "b = 2\n",
			Lazy:   true,
		},
		{
			Name: "toml lazy instant",
			Build: func() (Tree, error) {
				return NewLazyInstantTOMLTree("t", "a = 1\n", NewDirective("/", replaceProc("b = 2\n")))
			},
			Expect: "b = 2\n",
			Lazy:   true,
		},
		{
			Name: "toml lazy cache",
			Build: func() (Tree, error) {
				return NewLazyCacheTOMLTree("t", "a = 1\n", time.Minute, NewDirective("/", replaceProc("b = 2\n")))
			},
			Expect: "b = 2\n",
			Lazy:   true,
			TTL:    time.Minute,
		},
		{
			Name: "tile standard",
			Build: func() (Tree, error) {
				return NewTileTree("t", "tile", NewDirective("/", replaceProc("brick")))
			},
			Expect: "brick",
		},
		{
			Name: "tile lazy",
			Build: func() (Tree, error) {
				return NewLazyTileTree("t", "tile", NewDirective("/", replaceProc("brick")))
			},
			Expect: "brick",
			Lazy:   true,
		},
		{
			Name: "tile lazy instant",
			Build: func() (Tree, error) {
				return NewLazyInstantTileTree("t", "tile", NewDirective("/", replaceProc("brick")))
			},
			Expect: "brick",
			Lazy:   true,
		},
		{
			Name: "tile lazy cache",
			Build: func() (Tree, error) {
				return NewLazyCacheTileTree("t", "tile", time.Minute, NewDirective("/", replaceProc("brick")))
			},
			Expect: "brick",
			Lazy:   true,
			TTL:    time.Minute,
		},
	}

	for _, item := range testcases {
		t.Run(item.Name, func(t *testing.T) {
			tree, err := item.Build()
			if err != nil {
				t.Fatalf("build fail: %s", err)
			}

			val, err := tree.Get("/")
			if err != nil {
				t.Fatalf("get fail: %s", err)
			}
			if string(val) != item.Expect {
				t.Errorf("expect %q, got %q", item.Expect, val)
			}
		})
	}
}

func TestTreeConstructor_BuildError(t *testing.T) {
	boom := errors.New("boom")
	fail := &driver.RawProcessor{Proc: func(_ *driver.RealizeContext, _ []byte) ([]byte, error) {
		return nil, boom
	}}

	// standard mode realizes during build, so the error surfaces from the constructor
	if _, err := NewTree(newTestDriver(), "failing", `{}`, NewDirective("/", fail)); err == nil {
		t.Error("expected build error from failing processor")
	} else if !errors.Is(err, boom) {
		t.Errorf("expected wrapped boom error, got %v", err)
	}
}

func TestNewDirective_Accessors(t *testing.T) {
	proc := replaceProc("x")
	d := NewDirective("/a/b", proc)

	if d.Path() != "/a/b" {
		t.Errorf("unexpected path: %s", d.Path())
	}
	if len(d.Processors()) != 1 || d.Processors()[0] != proc {
		t.Errorf("unexpected processors: %v", d.Processors())
	}
}

func TestLazyCacheTree_TTLConstructor(t *testing.T) {
	// cache variant through the generic constructor used by format helpers
	tree, err := NewLazyCacheTree(newTestDriver(), "ttl", `{}`, 30*time.Millisecond,
		NewDirective("/", replaceProc("first")))
	if err != nil {
		t.Fatalf("build fail: %s", err)
	}

	v1, _ := tree.Get("/")
	time.Sleep(50 * time.Millisecond)
	v2, _ := tree.Get("/")
	if string(v1) != "first" || string(v2) != "first" {
		t.Errorf("unexpected values: %s / %s", v1, v2)
	}
}
