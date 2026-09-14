package driver

import (
	"encoding/json"
	"fmt"
	"regexp"
	"time"
)

var _ Processor = (*TemplateProcessor)(nil)
var _ ParamAware = (*TemplateProcessor)(nil)

// templatePattern matches ${key} placeholders in templates and content.
var templatePattern = regexp.MustCompile(`\$\{([a-zA-Z0-9_.-]+)\}`)

// TemplateProcessor interpolates ${key} placeholders with values from
// RealizeContext.Params at query time.
//
// It is param-aware, so it always runs in the dynamic layer of a node:
// its output is computed per query and never cached on the tree.
//
// Two modes:
//   - Pattern empty (default): placeholders found in the incoming content
//     are substituted in place. This composes with content inheritance —
//     a template can carry placeholders at the root and any queried node
//     renders them with the request's params.
//   - Pattern set: the rendered pattern replaces the content entirely.
type TemplateProcessor struct {
	// P is the target path of the Processor
	P string `json:"path"`

	// Pattern is an optional template; when set, its rendering replaces
	// the node content instead of interpolating the incoming content.
	Pattern string `json:"pattern,omitempty"`

	// A is the author of the Processor
	A string `json:"author"`
	// C is the create time of the Processor
	C time.Time `json:"created_at"`
}

// Type returns "template".
func (op *TemplateProcessor) Type() string { return "template" }

// Path returns the target tree path of the Processor.
func (op *TemplateProcessor) Path() string { return op.P }

// Author returns the processor author.
func (op *TemplateProcessor) Author() string { return op.A }

// CreatedAt returns the processor creation time.
func (op *TemplateProcessor) CreatedAt() time.Time { return op.C }

// ParamKeys returns the placeholder keys declared by Pattern.
// In content-interpolation mode (no Pattern) the keys live in the content
// and cannot be listed statically; the empty result still marks the
// processor as param-aware.
func (op *TemplateProcessor) ParamKeys() []string {
	return templateKeys(op.Pattern)
}

func templateKeys(s string) []string {
	matches := templatePattern.FindAllStringSubmatch(s, -1)
	keys := make([]string, 0, len(matches))
	for _, match := range matches {
		keys = append(keys, match[1])
	}
	return keys
}

// Load populates the processor from its JSON serialization.
func (op *TemplateProcessor) Load(data []byte) error {
	if err := json.Unmarshal(data, op); err != nil {
		return fmt.Errorf("unmarshal fail: %w", err)
	}
	return nil
}

// Save returns the JSON serialization of the processor.
func (op *TemplateProcessor) Save() []byte {
	data, _ := json.Marshal(op)
	return data
}

// Process renders the pattern or interpolates content placeholders with request params.
func (op *TemplateProcessor) Process(rc *RealizeContext, before []byte) (after []byte, err error) {
	params := Params(rc)
	if op.Pattern != "" {
		return []byte(Render(op.Pattern, params)), nil
	}
	return []byte(Render(string(before), params)), nil
}

// Params safely extracts the params map from a RealizeContext.
func Params(rc *RealizeContext) map[string]string {
	if rc == nil {
		return nil
	}
	return rc.Params
}

// IsParamAware reports whether proc's output depends on RealizeContext.Params,
// looking through *CombinedProcessor chains: a combined processor whose inner
// chain contains a param-aware processor is itself param-aware, while fully
// static combinations keep their static (cacheable) treatment.
//
// Engine code splitting a processor chain into a cacheable static prefix and
// a per-request dynamic layer must use this helper instead of a plain
// ParamAware type assertion, otherwise param-dependent output produced by a
// combined chain is cached and served unchanged to requests with different
// params.
func IsParamAware(proc Processor) bool {
	if proc == nil {
		return false
	}
	if _, ok := proc.(ParamAware); ok {
		return true
	}
	if combined, ok := proc.(*CombinedProcessor); ok {
		for _, inner := range combined.procs {
			if IsParamAware(inner) {
				return true
			}
		}
	}
	return false
}

// Render substitutes every ${key} placeholder in tpl with the matching
// param value. Missing keys render as empty strings, so an unparametrized
// query yields the template with placeholders stripped.
func Render(tpl string, params map[string]string) string {
	return templatePattern.ReplaceAllStringFunc(tpl, func(match string) string {
		key := match[2 : len(match)-1]
		if v, ok := params[key]; ok {
			return v
		}
		return ""
	})
}
