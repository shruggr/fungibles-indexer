package bitcom

import (
	"bytes"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"unicode/utf8"

	"github.com/libsv/go-bt/v2/bscript"
	"github.com/redis/go-redis/v9"
	"github.com/shruggr/fungibles-indexer/lib"
)

type Map map[string]interface{}

func (m *Map) Tag() string {
	return "map"
}
func (m *Map) Save(*lib.IndexContext, redis.Cmdable, *lib.Txo)     {}
func (m *Map) SetSpend(*lib.IndexContext, redis.Cmdable, *lib.Txo) {}
func (m *Map) AddLog(logName string, log map[string]string)        {}
func (m *Map) Logs() map[string]map[string]string                  {}
func (m *Map) IndexBySpent(idxName string, idxValue string)        {}
func (m *Map) OutputIndex() map[string][]string                    {}
func (m *Map) IndexSpend(idxName string, idxValue string)          {}
func (m *Map) SpendIndex() map[string][]string                     {}

func (m Map) Value() (driver.Value, error) {
	if m == nil {
		return nil, nil
	}
	return json.Marshal(m)
}

func (m *Map) Scan(value interface{}) error {
	b, ok := value.([]byte)
	if !ok {
		return errors.New("type assertion to []byte failed")
	}
	return json.Unmarshal(b, &m)
}

func ParseMAP(script *bscript.Script, idx *int) *Map {
	op, err := lib.ReadOp(*script, idx)
	if err != nil {
		return nil
	}
	if string(op.Data) != "SET" {
		return nil
	}
	mp := Map{}
	for {
		prevIdx := *idx
		op, err = lib.ReadOp(*script, idx)
		if err != nil || op.OpCode == bscript.OpRETURN || (op.OpCode == 1 && op.Data[0] == '|') {
			*idx = prevIdx
			break
		}
		opKey := op.Data
		prevIdx = *idx
		op, err = lib.ReadOp(*script, idx)
		if err != nil || op.OpCode == bscript.OpRETURN || (op.OpCode == 1 && op.Data[0] == '|') {
			*idx = prevIdx
			break
		}

		if len(opKey) > 256 || len(op.Data) > 1024 {
			continue
		}

		if !utf8.Valid(opKey) || !utf8.Valid(op.Data) {
			continue
		}

		if len(opKey) == 1 && opKey[0] == 0 {
			opKey = []byte{}
		}
		if len(op.Data) == 1 && op.Data[0] == 0 {
			op.Data = []byte{}
		}

		mp[string(opKey)] = string(op.Data)

	}
	if val, ok := mp["subTypeData"].(string); ok {
		if bytes.Contains([]byte(val), []byte{0}) || bytes.Contains([]byte(val), []byte("\\u0000")) {
			delete(mp, "subTypeData")
		} else {
			var subTypeData json.RawMessage
			if err := json.Unmarshal([]byte(val), &subTypeData); err == nil {
				mp["subTypeData"] = subTypeData
			}
		}
	}

	return &mp
}
