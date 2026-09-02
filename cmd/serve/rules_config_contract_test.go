package main

import (
	"os"
	"testing"
)

// TestLoad_DefaultRulesFile pins the shipped default conf/rules.json against
// config-key drift: the JSON keys must match the RuleDataItem tags so that
// load() actually produces a non-empty processor chain.
//
// Background: commit 8e60da4 (2023-10-18) renamed the struct tag from
// "operators" to "Processors" but missed conf/rules.json. encoding/json
// silently ignores mismatched keys, so the default config loaded an empty
// directive with no error — invisible to TestLoad, which only reads fixtures
// it writes itself.
func TestLoad_DefaultRulesFile(t *testing.T) {
	const defaultRules = "../../conf/rules.json"

	if _, err := os.Stat(defaultRules); err != nil {
		t.Skipf("default rules file not found (running from unexpected CWD?): %s", err)
	}

	t.Setenv("RULES_FILE", defaultRules)
	directives := load()
	if len(directives) == 0 {
		t.Fatal("default conf/rules.json must load at least one directive — " +
			"check for json key drift against RuleDataItem tags (e.g. \"operators\" vs \"Processors\")")
	}

	for _, d := range directives {
		if len(d.Processors()) == 0 {
			t.Errorf("directive %q loaded with 0 processors from default rules file — "+
				"a key-name drift makes encoding/json silently drop the array", d.Path())
		}
	}

	// The shipped demo config wires a curl processor at "/".
	if directives[0].Path() != "/" {
		t.Errorf("unexpected first path in default rules: %s", directives[0].Path())
	}
	if procs := directives[0].Processors(); procs[0].Type() != "curl" {
		t.Errorf("unexpected first processor type: %s", procs[0].Type())
	}
}
