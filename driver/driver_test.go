package driver_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/tr1v3r/ivy/driver"
)

func TestSlashPathParser(t *testing.T) {
	var testcases = []struct {
		path  string
		level int
	}{
		{"", 0},
		{"/", 0},
		{"/a/", 1},
		{"/a", 1},
		{"a/", 1},
		{"a", 1},
		{"/a/b/", 2},
		{"a/b/", 2},
		{"/a/b", 2},
		{"a/b", 2},
		{"/a/b/c/", 3},
		{"a/b/c/", 3},
		{"/a/b/c", 3},
		{"a/b/c", 3},
	}

	for _, item := range testcases {
		if l := driver.SlashPathParser.GetLevel(item.path); l != item.level {
			t.Errorf("get level expected %d, got %d", item.level, l)
		}
	}
}

func TestDriver_Marshal(t *testing.T) {
	var buf = make([]json.RawMessage, 0, 8)
	for i := range [8]int{} {
		buf = append(buf, json.RawMessage(fmt.Sprintf(`{"index":%d}`, i)))
	}
	data, _ := json.Marshal(buf)
	t.Logf("got result: %s", string(data))

	var nextBuf = make([]json.RawMessage, 0, 8)
	if err := json.Unmarshal(data, &nextBuf); err != nil {
		t.Errorf("unmarshal fail: %s", err)
	}
	for _, v := range nextBuf {
		t.Logf("got result: %s", string(v))
	}
}

func TestJSONDriver(t *testing.T) {
	var rule []byte

	d := driver.NewJSONDriver()

	data, err := d.Marshal([]driver.Processor{
		&driver.JSONProcessor{T: "create", JSONPath: "name.first", V: []byte("river")},
		&driver.JSONProcessor{T: "create", JSONPath: "name.last", V: []byte("chu")},
		&driver.JSONProcessor{T: "create", JSONPath: "name.last", V: []byte("Chu")},
		&driver.JSONProcessor{T: "append", JSONPath: "dear.friends.-1", V: []byte("tom")},
		&driver.JSONProcessor{T: "append", JSONPath: "dear.friends.-1", V: []byte("ken")},
		&driver.JSONProcessor{T: "set", JSONPath: "dear.family", V: []byte(`["mom","dad","bro"]`)},
		&driver.JSONProcessor{T: "create", JSONPath: "name.verbose", V: []byte("verbose")},
		&driver.JSONProcessor{T: "delete", JSONPath: "name.verbose"},
	}...)
	if err != nil {
		t.Errorf("marshal fail: %s", err)
		return
	}
	ops, err := d.Unmarshal(data)
	if err != nil {
		t.Errorf("unmarshal fail: %s", err)
		return
	}
	for _, op := range ops {
		rule, err = op.Process(nil, rule)
		if err != nil {
			t.Errorf("Process fail: %s", err)
			return
		}
	}
	t.Logf("got result: %s", rule)
}

func TestYAMLProcessor(t *testing.T) {
	var rule = []byte(`name: ivy
kind: engine
`)
	var err error

	var ops = []driver.Processor{
		&driver.RawProcessor{Proc: func(_ *driver.RealizeContext, before []byte) ([]byte, error) {
			var result any
			if err := yaml.Unmarshal(before, &result); err != nil {
				return nil, fmt.Errorf("unmarshal rule fail: %w", err)
			}
			result.(map[string]any)["unit"] = "test"
			newData, err := yaml.Marshal(result)
			return newData, err
		}},
	}
	for _, op := range ops {
		rule, err = op.Process(nil, rule)
		if err != nil {
			t.Errorf("Process fail: %s", err)
			return
		}
	}
	if !strings.Contains(string(rule), "unit: test") {
		t.Errorf("expected unit=test in result, got: %s", rule)
	}
	t.Logf("got result: %s", rule)
}

func TestTileDriver(t *testing.T) {
	var rule []byte

	d := driver.NewTileDriver()

	rule, err := d.Realize(nil, rule, &driver.RawProcessor{Proc: func(_ *driver.RealizeContext, _ []byte) ([]byte, error) { return []byte("abc\ndef"), nil }})
	if err != nil {
		t.Errorf("realize fail: %s", rule)
	}
	t.Logf("got result: %s", rule)
}
