package ord

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fxamacker/cbor"
	"github.com/libsv/go-bt/v2/bscript"
	"github.com/redis/go-redis/v9"
	"github.com/shruggr/fungibles-indexer/lib"
	"github.com/shruggr/fungibles-indexer/mod/bitcom"
)

type Inscription struct {
	lib.Indexable
	Json      json.RawMessage        `json:"json,omitempty"`
	Text      string                 `json:"text,omitempty"`
	Words     []string               `json:"words,omitempty"`
	File      *lib.File              `json:"file,omitempty"`
	Pointer   *uint64                `json:"pointer,omitempty"`
	Parent    *lib.Outpoint          `json:"parent,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	Metaproto []byte                 `json:"metaproto,omitempty"`
	Fields    map[string]interface{} `json:"-"`
}

func (i *Inscription) Tag() string {
	return "insc"
}

func (i *Inscription) Save(txCtx *lib.IndexContext, cmdable redis.Cmdable, txo *lib.Txo) {
	scoreHeight := txCtx.Height
	if scoreHeight == 0 {
		scoreHeight = uint32(time.Now().Unix())
	}
	if score, err := strconv.ParseFloat(fmt.Sprintf("%d.%010d", scoreHeight, txCtx.Idx), 64); err != nil {
		panic(err)
	} else {
		i.IndexByScore("seq", txo.Outpoint.String(), score)
	}
}

func ParseScript(txo *lib.Txo) {
	vout := txo.Outpoint.Vout()
	script := *txo.Tx.Outputs[vout].LockingScript

	for i := 0; i < len(script); {
		startI := i
		if op, err := lib.ReadOp(script, &i); err != nil {
			break
		} else if op.OpCode == bscript.OpDATA3 && i > 2 && bytes.Equal(op.Data, []byte("ord")) && script[startI-2] == 0 && script[startI-1] == bscript.OpIF {
			ParseInscription(txo, script, &i)
		}
	}
}

func ParseInscription(txo *lib.Txo, script []byte, fromPos *int) {
	pos := *fromPos
	ins := &Inscription{
		File: &lib.File{},
	}

ordLoop:
	for {
		op, err := lib.ReadOp(script, &pos)
		if err != nil || op.OpCode > bscript.Op16 {
			return
		}

		op2, err := lib.ReadOp(script, &pos)
		if err != nil || op2.OpCode > bscript.Op16 {
			return
		}

		var field int
		if op.OpCode > bscript.OpPUSHDATA4 && op.OpCode <= bscript.Op16 {
			field = int(op.OpCode) - 80
		} else if op.Len == 1 {
			field = int(op.Data[0])
		} else if op.Len > 1 {
			if ins.Fields == nil {
				ins.Fields = map[string]interface{}{}
			}
			if op.Len <= 64 && utf8.Valid(op.Data) && !bytes.Contains(op.Data, []byte{0}) && !bytes.Contains(op.Data, []byte("\\u0000")) {
				ins.Fields[string(op.Data)] = op2.Data
			}
			if string(op.Data) == bitcom.MAP {
				script := bscript.NewFromBytes(op2.Data)
				pos := 0
				md := bitcom.ParseMAP(*script, &pos)
				if md != nil {
					txo.AddData("map", md)
				}
			}
			continue
		}

		switch field {
		case 0:
			ins.File.Content = op2.Data
			break ordLoop
		case 1:
			if len(op2.Data) < 256 && utf8.Valid(op2.Data) {
				ins.File.Type = string(op2.Data)
			}
		case 2:
			pointer := binary.LittleEndian.Uint64(op2.Data)
			ins.Pointer = &pointer
		case 3:
			if parent, err := lib.NewOutpointFromTxOutpoint(op2.Data); err == nil {
				ins.Parent = parent
			}
		case 5:
			md := &bitcom.Map{}
			if err := cbor.Unmarshal(op2.Data, md); err == nil {
				ins.Metadata = *md
			}
		case 7:
			ins.Metaproto = op2.Data
		case 9:
			ins.File.Encoding = string(op2.Data)
		default:
			if ins.Fields == nil {
				ins.Fields = bitcom.Map{}
			}

		}
	}
	op, err := lib.ReadOp(script, &pos)
	if err != nil || op.OpCode != bscript.OpENDIF {
		return
	}
	*fromPos = pos

	ins.File.Size = uint32(len(ins.File.Content))
	hash := sha256.Sum256(ins.File.Content)
	ins.File.Hash = hash[:]
	insType := "file"
	if ins.File.Size <= 1024 && utf8.Valid(ins.File.Content) && !bytes.Contains(ins.File.Content, []byte{0}) && !bytes.Contains(ins.File.Content, []byte("\\u0000")) {
		mime := strings.ToLower(ins.File.Type)
		if strings.HasPrefix(mime, "application") ||
			strings.HasPrefix(mime, "text") {

			var data json.RawMessage
			err := json.Unmarshal(ins.File.Content, &data)
			if err == nil {
				insType = "json"
				ins.Json = data
				// bsv20, _ = ParseBsv20Inscription(ins.File, txo)
			} else if AsciiRegexp.Match(ins.File.Content) {
				if insType == "file" {
					insType = "text"
				}
				ins.Text = string(ins.File.Content)
				re := regexp.MustCompile(`\W`)
				words := map[string]struct{}{}
				for _, word := range re.Split(ins.Text, -1) {
					if len(word) > 0 {
						word = strings.ToLower(word)
						words[word] = struct{}{}
					}
				}
				if len(words) > 0 {
					ins.Words = make([]string, 0, len(words))
					for word := range words {
						ins.Words = append(ins.Words, word)
					}
				}
			}
		}
	}

	if txo.PKHash != nil && len(*txo.PKHash) == 0 {
		if len(script) >= pos+25 && bscript.NewFromBytes(script[pos:pos+25]).IsP2PKH() {
			pkhash := lib.PKHash(script[pos+3 : pos+23])
			txo.PKHash = &pkhash
		} else if len(script) >= pos+26 &&
			script[pos] == bscript.OpCODESEPARATOR &&
			bscript.NewFromBytes(script[pos+1:pos+26]).IsP2PKH() {
			pkhash := lib.PKHash(script[pos+4 : pos+24])
			txo.PKHash = &pkhash
		}
	}

	txo.AddData("insc", ins)
}
