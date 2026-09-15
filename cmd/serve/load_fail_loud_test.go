package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// #58 (W5): load() must never degrade silently. File-level failures
// return an error (startup aborts, SIGHUP keeps the old forest — see
// serve.go); processor-level failures drop the offending op instead of
// appending a half-unmarshaled zero-value processor to the chain, where
// it would emit invalid content with no error. These tests pin the
// op-level half; the file-level half lives in serve_test.go
// (TestLoad_DefaultFileMissing / TestLoad_InvalidJSON).

// writeRulesFile installs raw rules content under a temp file.
func writeRulesFile(t *testing.T, content string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "rules.json")
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatalf("write rules fail: %s", err)
	}
	return file
}

// A processor whose Load fails (value 123 cannot unmarshal into the
// json processor's []byte value — it fails partially with T/V set but
// V empty) must be dropped from the chain, while its healthy sibling
// op and every other directive keep working.
//
// On unfixed master the failed op was still appended (after a Warn), so
// the /tainted chain carried the half-initialized processor that
// silently produced invalid JSON — this test fails there.
func TestLoad_FailedOpDropped_SiblingsKept(t *testing.T) {
	rules := `[
		{"path":"/good","Processors":[
			{"type":"json","data":{"path":"g","type":"create","value":"MQ=="}}
		]},
		{"path":"/tainted","Processors":[
			{"type":"json","data":{"path":"t","type":"set","value":123}},
			{"type":"json","data":{"path":"g","type":"create","value":"MQ=="}}
		]}
	]`
	t.Setenv("RULES_FILE", writeRulesFile(t, rules))

	directives, err := load()
	if err != nil {
		t.Fatalf("op-level failures must not fail the file load: %s", err)
	}
	if len(directives) != 2 {
		t.Fatalf("both directives must survive, got %d (%s)", len(directives), directivePaths(directives))
	}
	if directives[0].Path() != "/good" || directives[1].Path() != "/tainted" {
		t.Fatalf("unexpected directive order: %s", directivePaths(directives))
	}

	// /good: its only op is valid, untouched
	if procs := directives[0].Processors(); len(procs) != 1 || procs[0] == nil {
		t.Fatalf("/good must keep its 1 loaded processor, got %d", len(procs))
	}

	// /tainted: only the healthy op survives; the failed op is NOT in
	// the chain as a zero value
	procs := directives[1].Processors()
	if len(procs) != 1 {
		t.Fatalf("/tainted must keep only the loaded op, got %d — a Load-failed processor was appended", len(procs))
	}
	if procs[0] == nil {
		t.Fatal("surviving processor must be the loaded one, got nil")
	}
	if procs[0].Path() != "g" || procs[0].Type() != "create" {
		t.Errorf("surviving processor identity: path=%q type=%q, want path=g type=create",
			procs[0].Path(), procs[0].Type())
	}
}

// A directive whose processors all fail to Load is dropped entirely;
// the rest of the rules file still loads.
func TestLoad_AllOpsFailed_DirectiveDropped(t *testing.T) {
	rules := `[
		{"path":"/doomed","Processors":[
			{"type":"json","data":{"path":"a","type":"set","value":123}},
			{"type":"json","data":{"path":"b","type":"set","value":"not base64!"}}
		]},
		{"path":"/fine","Processors":[
			{"type":"json","data":{"path":"g","type":"create","value":"MQ=="}}
		]}
	]`
	t.Setenv("RULES_FILE", writeRulesFile(t, rules))

	directives, err := load()
	if err != nil {
		t.Fatalf("load fail: %s", err)
	}
	if len(directives) != 1 || directives[0].Path() != "/fine" {
		t.Fatalf("/doomed must be dropped and /fine kept, got %s", directivePaths(directives))
	}
}

// An explicitly empty "Processors": [] is a valid configuration, not a
// failure — the directive stays (no over-drop).
func TestLoad_ExplicitlyEmptyProcessorsKept(t *testing.T) {
	t.Setenv("RULES_FILE", writeRulesFile(t, `[{"path":"/empty","Processors":[]}]`))

	directives, err := load()
	if err != nil {
		t.Fatalf("load fail: %s", err)
	}
	if len(directives) != 1 || directives[0].Path() != "/empty" {
		t.Fatalf("explicitly empty directive must be kept, got %s", directivePaths(directives))
	}
	if procs := directives[0].Processors(); len(procs) != 0 {
		t.Errorf("expected 0 processors, got %d", len(procs))
	}
}

// File-level failures must surface as errors that name the file, so
// startup can abort loudly instead of serving an empty tree. (Missing
// file and invalid JSON are covered in serve_test.go; this pins the
// error message contract for both.)
func TestLoad_FileErrorsNameTheFile(t *testing.T) {
	dir := t.TempDir()

	t.Setenv("RULES_FILE", filepath.Join(dir, "absent.json"))
	_, err := load()
	if err == nil {
		t.Fatal("missing file must be an error")
	}
	if !strings.Contains(err.Error(), "absent.json") {
		t.Errorf("error must name the file, got: %s", err)
	}

	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`[{"path":`), 0o600); err != nil {
		t.Fatalf("write fail: %s", err)
	}
	t.Setenv("RULES_FILE", bad)
	_, err = load()
	if err == nil {
		t.Fatal("torn json must be an error")
	}
	if !strings.Contains(err.Error(), "bad.json") {
		t.Errorf("error must name the file, got: %s", err)
	}
}
