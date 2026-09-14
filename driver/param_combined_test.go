package driver_test

import (
	"testing"

	"github.com/tr1v3r/ivy/driver"
)

// Regression coverage for audit finding H4: IsParamAware must look through
// CombinedProcessor chains, and fully static combinations must stay static.
func TestIsParamAware(t *testing.T) {
	static := driver.CombineProcessor(&driver.RawProcessor{Proc: func(_ *driver.RealizeContext, b []byte) ([]byte, error) {
		return b, nil
	}})
	paramAware := &driver.TemplateProcessor{Pattern: "user=${user}"}
	combined := driver.CombineProcessor(
		&driver.RawProcessor{Proc: func(_ *driver.RealizeContext, b []byte) ([]byte, error) {
			return b, nil
		}},
		paramAware,
	)
	nested := driver.CombineProcessor(driver.CombineProcessor(paramAware))

	var cases = []struct {
		name string
		proc driver.Processor
		want bool
	}{
		{"nil", nil, false},
		{"template", paramAware, true},
		{"static combined", static, false},
		{"combined with template", combined, true},
		{"nested combined with template", nested, true},
		{"combined with nil inner", driver.CombineProcessor(nil, paramAware), true},
	}
	for _, item := range cases {
		if got := driver.IsParamAware(item.proc); got != item.want {
			t.Errorf("IsParamAware(%s) = %v, want %v", item.name, got, item.want)
		}
	}
}
