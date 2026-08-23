package driver_test

import (
	"strings"
	"testing"
	"time"

	"github.com/tr1v3r/ivy/driver"
)

func TestTemplateProcessorMetadata(t *testing.T) {
	created := time.Date(2024, 7, 1, 0, 0, 0, 0, time.UTC)
	op := &driver.TemplateProcessor{P: "/t", A: "river", C: created}

	if op.Type() != "template" {
		t.Errorf("unexpected type: %s", op.Type())
	}
	if op.Path() != "/t" || op.Author() != "river" || !op.CreatedAt().Equal(created) {
		t.Errorf("unexpected metadata: %q %q %v", op.Path(), op.Author(), op.CreatedAt())
	}

	var restored driver.TemplateProcessor
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

func TestTemplateProcessorParamAware(t *testing.T) {
	op := &driver.TemplateProcessor{Pattern: "hello ${user}, welcome to ${env}"}

	// implements the optional interface
	var aware driver.ParamAware = op
	keys := aware.ParamKeys()
	if len(keys) != 2 || keys[0] != "user" || keys[1] != "env" {
		t.Errorf("unexpected ParamKeys: %v", keys)
	}

	// content-interpolation mode cannot list keys statically; still param-aware
	if keys := (&driver.TemplateProcessor{}).ParamKeys(); len(keys) != 0 {
		t.Errorf("expected no static keys in interpolation mode, got %v", keys)
	}
}

func TestTemplateProcessorInterpolateContent(t *testing.T) {
	before := []byte(`{"greeting": "hello ${user}", "env": "${env}"}`)
	rc := &driver.RealizeContext{Params: map[string]string{"user": "alice", "env": "prod"}}

	after, err := (&driver.TemplateProcessor{}).Process(rc, before)
	if err != nil {
		t.Fatalf("Process fail: %s", err)
	}
	s := string(after)
	if !strings.Contains(s, "hello alice") {
		t.Errorf("expected user interpolated, got: %s", s)
	}
	if !strings.Contains(s, `"env": "prod"`) {
		t.Errorf("expected env interpolated, got: %s", s)
	}

	// missing keys render as empty strings
	after, _ = (&driver.TemplateProcessor{}).Process(&driver.RealizeContext{}, before)
	if !strings.Contains(string(after), `"env": ""`) {
		t.Errorf("expected empty env without params, got: %s", after)
	}

	// nil context is safe
	after, err = (&driver.TemplateProcessor{}).Process(nil, before)
	if err != nil {
		t.Fatalf("Process(nil) fail: %s", err)
	}
	if strings.Contains(string(after), "${") {
		t.Errorf("expected placeholders stripped with nil rc, got: %s", after)
	}
}

func TestTemplateProcessorPatternMode(t *testing.T) {
	op := &driver.TemplateProcessor{Pattern: "user=${user}&page=${page}"}
	rc := &driver.RealizeContext{Params: map[string]string{"user": "bob", "page": "3"}}

	after, err := op.Process(rc, []byte("ignored content"))
	if err != nil {
		t.Fatalf("Process fail: %s", err)
	}
	if string(after) != "user=bob&page=3" {
		t.Errorf("unexpected rendered pattern: %s", after)
	}
}

func TestRender(t *testing.T) {
	params := map[string]string{"a": "1"}
	var testcases = []struct {
		Tpl  string
		Want string
	}{
		{"${a}", "1"},
		{"x${a}y", "x1y"},
		{"${missing}", ""},
		{"no placeholders", "no placeholders"},
		{"${a}${a}", "11"},
		{"${}", "${}"}, // empty key does not match the pattern
	}
	for _, item := range testcases {
		if got := driver.Render(item.Tpl, params); got != item.Want {
			t.Errorf("Render(%q): expect %q, got %q", item.Tpl, item.Want, got)
		}
	}
}
