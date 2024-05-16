package bitcom

import (
	"github.com/libsv/go-bt/v2"
	"github.com/shruggr/fungibles-indexer/lib"
)

var MAP = "1PuQa7K62MiKCtssSLKy1kh56WWU7MtUR5"
var B = "19HxigV4QyBv3tHpQVcUEQyq1pzZVdoAut"

func ParseBitcom(tx *bt.Tx, vout uint32, idx *int) (value lib.IIndexable, err error) {
	script := *tx.Outputs[vout].LockingScript

	startIdx := *idx
	op, err := lib.ReadOp(script, idx)
	if err != nil {
		return
	}
	switch string(op.Data) {
	case MAP:
		mod := ParseMAP(&script, idx)
		value = mod
	case B:
		mod := ParseB(&script, idx)
		value = mod
	case "SIGMA":
		mod := ParseSigma(tx, script, startIdx, idx)
		value = mod
	default:
		*idx--
	}
	return value, nil
}
