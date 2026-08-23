package driver

import "encoding/json"

var _ Driver = (*TileDriver)(nil)

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
