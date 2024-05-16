package bitcom

import (
	"crypto/sha256"
	"database/sql/driver"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/bitcoinschema/go-bitcoin"
	"github.com/libsv/go-bt/v2"
	"github.com/libsv/go-bt/v2/bscript"
	"github.com/shruggr/fungibles-indexer/lib"
)

type Sigmas []*Sigma

func (s Sigmas) Value() (driver.Value, error) {
	return json.Marshal(s)
}

func (s *Sigmas) Scan(value interface{}) error {
	b, ok := value.([]byte)
	if !ok {
		return errors.New("type assertion to []byte failed")
	}
	return json.Unmarshal(b, &s)
}

type Sigma struct {
	lib.Indexable
	Algorithm string `json:"algorithm"`
	Address   string `json:"address"`
	Signature []byte `json:"signature"`
	Vin       uint32 `json:"vin"`
	Valid     bool   `json:"valid"`
}

func (s *Sigma) Tag() string {
	return "sigma"
}

func ParseSigma(tx *bt.Tx, script bscript.Script, startIdx int, idx *int) (sigma *Sigma) {
	sigma = &Sigma{}
	for i := 0; i < 4; i++ {
		prevIdx := *idx
		op, err := lib.ReadOp(script, idx)
		if err != nil || op.OpCode == bscript.OpRETURN || (op.OpCode == 1 && op.Data[0] == '|') {
			*idx = prevIdx
			break
		}

		switch i {
		case 0:
			sigma.Algorithm = string(op.Data)
		case 1:
			sigma.Address = string(op.Data)
		case 2:
			sigma.Signature = op.Data
		case 3:
			vin, err := strconv.ParseInt(string(op.Data), 10, 32)
			if err == nil {
				sigma.Vin = uint32(vin)
			}
		}
	}

	outpoint := tx.Inputs[sigma.Vin].PreviousTxID()
	outpoint = binary.LittleEndian.AppendUint32(outpoint, tx.Inputs[sigma.Vin].PreviousTxOutIndex)
	inputHash := sha256.Sum256(outpoint)
	var scriptBuf []byte
	if script[startIdx-1] == bscript.OpRETURN {
		scriptBuf = script[:startIdx-1]
	} else if script[startIdx-1] == '|' {
		scriptBuf = script[:startIdx-2]
	} else {
		return nil
	}
	outputHash := sha256.Sum256(scriptBuf)
	msgHash := sha256.Sum256(append(inputHash[:], outputHash[:]...))
	err := bitcoin.VerifyMessage(sigma.Address,
		base64.StdEncoding.EncodeToString(sigma.Signature),
		string(msgHash[:]),
	)
	if err != nil {
		return nil
	}
	sigma.Valid = true
	return
}
