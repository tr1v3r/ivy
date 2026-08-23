package driver

import (
	"fmt"
	"strings"
)

var _ PathParser = (*DelimiterPathParser)(nil)

// SlashPathParser slash path parser
// equal to DelimiterPathParser{delimiter: "/"}
var SlashPathParser = new(DelimiterPathParser).WithDelimiter("/")

// DelimiterPathParser delimiter path parser
type DelimiterPathParser struct {
	delimiter string
}

// WithDelimiter returns a copy of the parser using the given delimiter.
func (d DelimiterPathParser) WithDelimiter(delimiter string) *DelimiterPathParser {
	d.delimiter = delimiter
	return &d
}

// GetLevel returns the number of path segments; empty paths are level 0.
func (d *DelimiterPathParser) GetLevel(path string) int {
	if path = strings.Trim(strings.TrimSpace(path), d.delimiter); path != "" {
		return len(strings.Split(path, d.delimiter))
	}
	return 0
}

// GetNameByLevel returns the segment at the given 1-based level, or "" when out of range.
func (d *DelimiterPathParser) GetNameByLevel(path string, level int) string {
	if path = strings.Trim(strings.TrimSpace(path), d.delimiter); path != "" {
		return d.getName(strings.Split(path, d.delimiter), level-1)
	}
	return ""
}

// AppendPath joins a name onto a path with the parser delimiter.
func (d *DelimiterPathParser) AppendPath(path, name string) string { return path + d.delimiter + name }
func (d *DelimiterPathParser) getName(paths []string, index int) string {
	if index >= len(paths) {
		return ""
	}
	return paths[index]
}

var _ Realizer = (*StdRealizer)(nil)

// StdRealizer standard rule driver
type StdRealizer struct{}

// Realize applies the processor chain to the rule content sequentially.
func (r *StdRealizer) Realize(rc *RealizeContext, rule []byte, procs ...Processor) ([]byte, error) {
	var err error
	for _, proc := range procs {
		if proc == nil {
			continue
		}
		if rule, err = proc.Process(rc, rule); err != nil {
			return nil, fmt.Errorf("do %s on %s fail: %w", proc.Type(), proc.Path(), err)
		}
	}
	return rule, nil
}
