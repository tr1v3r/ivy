package driver_test

import (
	"errors"
	"testing"
	"time"

	"github.com/tr1v3r/ivy/driver"
)

func TestJSONProcessorMetadata(t *testing.T) {
	created := time.Date(2023, 5, 17, 23, 0, 0, 0, time.UTC)
	op := &driver.JSONProcessor{P: "/a", T: "create", A: "river", C: created}

	if op.Type() != "create" {
		t.Errorf("unexpected type: %s", op.Type())
	}
	if op.Path() != "/a" {
		t.Errorf("unexpected path: %s", op.Path())
	}
	if op.Author() != "river" {
		t.Errorf("unexpected author: %s", op.Author())
	}
	if !op.CreatedAt().Equal(created) {
		t.Errorf("unexpected created_at: %v", op.CreatedAt())
	}

	// save/load roundtrip
	var restored driver.JSONProcessor
	if err := restored.Load(op.Save()); err != nil {
		t.Fatalf("load fail: %s", err)
	}
	if restored.Type() != "create" || restored.Path() != "/a" || restored.Author() != "river" {
		t.Errorf("roundtrip mismatch: %+v", restored)
	}
	if !restored.CreatedAt().Equal(created) {
		t.Errorf("roundtrip created_at mismatch: %v", restored.CreatedAt())
	}

	if err := restored.Load([]byte("not-json")); err == nil {
		t.Error("expected load error on invalid json")
	}

	// unknown processor type errors in Process
	if _, err := (&driver.JSONProcessor{T: "wat"}).Process(nil, []byte(`{}`)); err == nil {
		t.Error("expected error for unknown json processor type")
	}
}

func TestYAMLProcessorMetadataAndProcess(t *testing.T) {
	created := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	op := &driver.YAMLProcessor{P: "/y", T: "append", A: "river", C: created}

	if op.Type() != "append" {
		t.Errorf("unexpected type: %s", op.Type())
	}
	if op.Path() != "/y" || op.Author() != "river" {
		t.Errorf("unexpected path/author: %q %q", op.Path(), op.Author())
	}
	if !op.CreatedAt().Equal(created) {
		t.Errorf("unexpected created_at: %v", op.CreatedAt())
	}

	// roundtrip
	var restored driver.YAMLProcessor
	if err := restored.Load(op.Save()); err != nil {
		t.Fatalf("load fail: %s", err)
	}
	if restored.Type() != "append" || restored.Path() != "/y" || restored.Author() != "river" {
		t.Errorf("roundtrip mismatch: %+v", restored)
	}
	if !restored.CreatedAt().Equal(created) {
		t.Errorf("roundtrip created_at mismatch: %v", restored.CreatedAt())
	}
	if err := restored.Load([]byte("not-json")); err == nil {
		t.Error("expected load error on invalid json")
	}

	// invalid yaml content surfaces the unmarshal error
	if _, err := (&driver.YAMLProcessor{T: "set", YAMLPath: "a"}).Process(nil, []byte("\tbad: [yaml")); err == nil {
		t.Error("expected error on invalid yaml content")
	}
	// empty path errors regardless of type
	if _, err := (&driver.YAMLProcessor{T: "set"}).Process(nil, []byte("a: 1")); err == nil {
		t.Error("expected error on empty yaml path")
	}
	// unknown type errors
	if _, err := (&driver.YAMLProcessor{T: "unknown", YAMLPath: "a"}).Process(nil, []byte("a: 1")); err == nil {
		t.Error("expected error for unknown yaml processor type")
	}
}

func TestYAMLDriverRoundtrip(t *testing.T) {
	d := driver.NewYAMLDriver()
	if d.Name() != "yaml" {
		t.Errorf("unexpected driver name: %s", d.Name())
	}

	data, err := d.Marshal(&driver.YAMLProcessor{T: "append", V: []byte("x")})
	if err != nil {
		t.Fatalf("marshal fail: %s", err)
	}

	ops, err := d.Unmarshal(data)
	if err != nil {
		t.Fatalf("unmarshal fail: %s", err)
	}
	if len(ops) != 1 || ops[0].Type() != "append" {
		t.Errorf("roundtrip mismatch: %+v", ops)
	}

	if _, err := d.Unmarshal([]byte("not-json")); err == nil {
		t.Error("expected unmarshal error on invalid json")
	}
}

func TestCURLProcessorMetadata(t *testing.T) {
	created := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	op := &driver.CURLProcessor{P: "/u", URL: "http://example.com", A: "bob", C: created}

	if op.Type() != "curl" {
		t.Errorf("unexpected type: %s", op.Type())
	}
	if op.Path() != "/u" {
		t.Errorf("unexpected path: %s", op.Path())
	}
	if op.Author() != "bob" {
		t.Errorf("unexpected author: %s", op.Author())
	}
	if !op.CreatedAt().Equal(created) {
		t.Errorf("unexpected created_at: %v", op.CreatedAt())
	}

	var restored driver.CURLProcessor
	if err := restored.Load(op.Save()); err != nil {
		t.Fatalf("load fail: %s", err)
	}
	if restored.URL != "http://example.com" || restored.Author() != "bob" {
		t.Errorf("roundtrip mismatch: %+v", restored)
	}
	if err := restored.Load([]byte("not-json")); err == nil {
		t.Error("expected load error on invalid json")
	}
}

func TestCURLProcessErrorUnwrap(t *testing.T) {
	sentinel := errors.New("dial tcp: refused")
	var err error = &driver.CURLProcessError{Method: "GET", URL: "http://x", Err: sentinel}

	if !errors.Is(err, sentinel) {
		t.Error("expected errors.Is to unwrap the cause")
	}
}

func TestRawProcessorMetadata(t *testing.T) {
	op := &driver.RawProcessor{}

	if op.Type() != "" || op.Path() != "" || op.Author() != "" {
		t.Errorf("unexpected metadata: %q %q %q", op.Type(), op.Path(), op.Author())
	}
	if !op.CreatedAt().IsZero() {
		t.Errorf("expected zero CreatedAt, got %v", op.CreatedAt())
	}
	if err := op.Load([]byte("{}")); !errors.Is(err, driver.ErrSerializeNotSupport) {
		t.Errorf("expected ErrSerializeNotSupport, got %v", err)
	}
	if op.Save() != nil {
		t.Error("expected nil Save payload")
	}
}

func TestCombinedProcessorMetadataAndProcess(t *testing.T) {
	upper := &driver.RawProcessor{Proc: func(_ *driver.RealizeContext, before []byte) ([]byte, error) {
		return append(before, '+'), nil
	}}
	combined := driver.CombineProcessor(nil, upper)

	if combined.Type() != "combined" {
		t.Errorf("unexpected type: %s", combined.Type())
	}
	if combined.Path() != "" || combined.Author() != "" {
		t.Errorf("unexpected path/author: %q %q", combined.Path(), combined.Author())
	}
	if !combined.CreatedAt().IsZero() {
		t.Errorf("expected zero CreatedAt, got %v", combined.CreatedAt())
	}
	if err := combined.Load([]byte("{}")); !errors.Is(err, driver.ErrSerializeNotSupport) {
		t.Errorf("expected ErrSerializeNotSupport, got %v", err)
	}
	if combined.Save() != nil {
		t.Error("expected nil Save payload")
	}

	// nil processors in the chain are skipped
	out, err := combined.Process(nil, []byte("a"))
	if err != nil || string(out) != "a+" {
		t.Errorf("process: out=%q err=%v", out, err)
	}

	// failures are wrapped with the failing processor's type
	boom := errors.New("boom")
	failing := driver.CombineProcessor(&driver.RawProcessor{Proc: func(_ *driver.RealizeContext, _ []byte) ([]byte, error) {
		return nil, boom
	}})
	if _, err := failing.Process(nil, nil); !errors.Is(err, boom) {
		t.Errorf("expected wrapped boom, got %v", err)
	}
}

func TestDummyDriverAndProcessor(t *testing.T) {
	d := driver.NewDummyDriver()
	if d.Name() != "dummy" {
		t.Errorf("unexpected driver name: %s", d.Name())
	}

	var op driver.DummyProcessor
	if op.Type() != "dummy" || op.Path() != "" || op.Author() != "dummy" {
		t.Errorf("unexpected metadata: %q %q %q", op.Type(), op.Path(), op.Author())
	}
	if op.CreatedAt().IsZero() {
		t.Error("expected non-zero CreatedAt")
	}
	if err := op.Load([]byte("{}")); err != nil {
		t.Errorf("load should be a no-op, got %v", err)
	}
	if op.Save() != nil {
		t.Error("expected nil Save payload")
	}
	if out, err := op.Process(nil, []byte("x")); out != nil || err != nil {
		t.Errorf("process should return nil/nil, got %q %v", out, err)
	}
}

func TestTOMLProcessorMetadata(t *testing.T) {
	created := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	op := &driver.TOMLProcessor{P: "/t", T: "create", A: "river", C: created}

	if op.Type() != "create" {
		t.Errorf("unexpected type: %s", op.Type())
	}
	if op.Path() != "/t" || op.Author() != "river" || !op.CreatedAt().Equal(created) {
		t.Errorf("unexpected metadata: %q %q %v", op.Path(), op.Author(), op.CreatedAt())
	}

	var restored driver.TOMLProcessor
	if err := restored.Load(op.Save()); err != nil {
		t.Fatalf("load fail: %s", err)
	}
	if restored.Path() != "/t" || restored.Author() != "river" {
		t.Errorf("roundtrip mismatch: %+v", restored)
	}
	if err := restored.Load([]byte("not-json")); err == nil {
		t.Error("expected load error on invalid json")
	}
}

func TestXMLProcessorMetadata(t *testing.T) {
	created := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	op := &driver.XMLProcessor{P: "/x", T: "create", A: "river", C: created}

	if op.Type() != "create" {
		t.Errorf("unexpected type: %s", op.Type())
	}
	if op.Path() != "/x" || op.Author() != "river" || !op.CreatedAt().Equal(created) {
		t.Errorf("unexpected metadata: %q %q %v", op.Path(), op.Author(), op.CreatedAt())
	}

	var restored driver.XMLProcessor
	if err := restored.Load(op.Save()); err != nil {
		t.Fatalf("load fail: %s", err)
	}
	if restored.Path() != "/x" || restored.Author() != "river" {
		t.Errorf("roundtrip mismatch: %+v", restored)
	}
	if err := restored.Load([]byte("not-json")); err == nil {
		t.Error("expected load error on invalid json")
	}
}

func TestDriverNames(t *testing.T) {
	var testcases = []struct {
		Driver driver.Driver
		Name   string
	}{
		{driver.NewJSONDriver(), "json"},
		{driver.NewYAMLDriver(), "yaml"},
		{driver.NewXMLDriver(), "xml"},
		{driver.NewTOMLDriver(), "toml"},
		{driver.NewTileDriver(), "tile"},
		{driver.NewDummyDriver(), "dummy"},
	}
	for _, item := range testcases {
		if item.Driver.Name() != item.Name {
			t.Errorf("expect driver name %q, got %q", item.Name, item.Driver.Name())
		}
	}
}

func TestPathParserNameByLevel(t *testing.T) {
	var testcases = []struct {
		Path  string
		Level int
		Name  string
	}{
		{"a/b/c", 1, "a"},
		{"a/b/c", 2, "b"},
		{"a/b/c", 3, "c"},
		{"/a/b", 1, "a"},
		{"a/b", 99, ""}, // out of range
		{"", 1, ""},     // empty path
		{"/", 1, ""},
	}
	for _, item := range testcases {
		if got := driver.SlashPathParser.GetNameByLevel(item.Path, item.Level); got != item.Name {
			t.Errorf("GetNameByLevel(%q, %d): expect %q, got %q", item.Path, item.Level, item.Name, got)
		}
	}

	if got := driver.SlashPathParser.AppendPath("/a", "b"); got != "/a/b" {
		t.Errorf("AppendPath: expect /a/b, got %q", got)
	}
}

func TestStdRealizerWrapsErrors(t *testing.T) {
	boom := errors.New("boom")
	r := new(driver.StdRealizer)

	failing := &driver.RawProcessor{Proc: func(_ *driver.RealizeContext, _ []byte) ([]byte, error) {
		return nil, boom
	}}
	if _, err := r.Realize(nil, []byte("x"), failing); !errors.Is(err, boom) {
		t.Errorf("expected wrapped boom, got %v", err)
	}

	// nil processors are skipped
	if out, err := r.Realize(nil, []byte("x"), nil); err != nil || string(out) != "x" {
		t.Errorf("nil processor should be skipped: out=%q err=%v", out, err)
	}
}
