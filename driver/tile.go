package driver

import "encoding/json"

var _ Driver = (*TileDriver)(nil)

var _ SegmentParser = (*TileDriver)(nil)

// ParseSegments forwards to the embedded parser when it supports segment
// parsing, letting the engine split a queried path once per descent
// instead of once per tree level. Custom embedded parsers without
// SegmentParser support report ok=false and keep the per-call path.
func (d TileDriver) ParseSegments(path string) ([]string, bool) {
	if sp, ok := d.PathParser.(SegmentParser); ok {
		return sp.ParseSegments(path)
	}
	return nil, false
}

// NewTileDriver creates a driver for raw tile content: slash-separated
// paths, plain realization and RawProcessor serialization.
func NewTileDriver() *TileDriver {
	return &TileDriver{
		PathParser: SlashPathParser,
		Realizer:   new(StdRealizer),
		Modem: &GeneralModem[*RawProcessor]{
			Marshaler:   json.Marshal,
			Unmarshaler: json.Unmarshal,
		},
	}
}

// TileDriver is a driver for raw type rule tree
type TileDriver struct {
	PathParser
	Realizer
	Modem
}

// Name returns "tile".
func (TileDriver) Name() string { return "tile" }
