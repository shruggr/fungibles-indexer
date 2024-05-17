package bitcom

import (
	"crypto/sha256"

	"github.com/libsv/go-bt/v2/bscript"
	"github.com/shruggr/fungibles-indexer/lib"
)

type BFile struct {
	lib.File
	lib.Indexable
}

func (b *BFile) Tag() string {
	return "b"
}

func ParseB(script []byte, idx *int) (b *BFile) {
	b = &BFile{}
	for i := 0; i < 4; i++ {
		prevIdx := *idx
		op, err := lib.ReadOp(script, idx)
		if err != nil || op.OpCode == bscript.OpRETURN || (op.OpCode == 1 && op.Data[0] == '|') {
			*idx = prevIdx
			break
		}

		switch i {
		case 0:
			b.Content = op.Data
		case 1:
			b.Type = string(op.Data)
		case 2:
			b.Encoding = string(op.Data)
		case 3:
			b.Name = string(op.Data)
		}
	}
	hash := sha256.Sum256(b.Content)
	b.Size = uint32(len(b.Content))
	b.Hash = hash[:]
	return
}
