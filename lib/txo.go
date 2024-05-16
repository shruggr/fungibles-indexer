package lib

import (
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/libsv/go-bt/v2"
	"github.com/redis/go-redis/v9"
)

type Txo struct {
	Outpoint    *Outpoint                    `json:"outpoint,omitempty"`
	Height      uint32                       `json:"height,omitempty"`
	Idx         uint64                       `json:"idx"`
	Satoshis    uint64                       `json:"satoshis"`
	Script      []byte                       `json:"script,omitempty"`
	OutAcc      uint64                       `json:"outacc"`
	PKHash      *PKHash                      `json:"pkhash"`
	Spend       *ByteString                  `json:"spend"`
	Vin         uint32                       `json:"vin"`
	SpendHeight uint32                       `json:"spend_height"`
	SpendIdx    uint64                       `json:"spend_idx"`
	Origin      *Outpoint                    `json:"origin,omitempty"`
	Data        map[string]IIndexable        `json:"data,omitempty"`
	Tx          *bt.Tx                       `json:"-"`
	logs        map[string]map[string]string `json:"-"`
}

func (t *Txo) ID() string {
	return "txo:" + t.Outpoint.String()
}

func (t *Txo) Map() (m map[string]interface{}, err error) {
	m = map[string]interface{}{
		"outpoint":    t.Outpoint.String(),
		"height":      t.Height,
		"idx":         t.Idx,
		"satoshis":    t.Satoshis,
		"outacc":      t.OutAcc,
		"script":      t.Script,
		"spend":       t.Spend,
		"spendHeight": t.SpendHeight,
	}
	if t.PKHash != nil {
		if m["owner"], err = t.PKHash.Address(); err != nil {
			return
		}
	}
	return
}

func NewTxoFromMap(m map[string]string) (t *Txo, err error) {
	t = &Txo{}
	for k, v := range m {
		switch k {
		case "outpoint":
			if t.Outpoint, err = NewOutpointFromString(v); err != nil {
				return
			}
		case "height":
			var height uint64
			if height, err = strconv.ParseUint(v, 10, 32); err != nil {
				return
			}
			t.Height = uint32(height)
		case "idx":
			if t.Idx, err = strconv.ParseUint(v, 10, 64); err != nil {
				return nil, err
			}
		case "satoshis":
			if t.Satoshis, err = strconv.ParseUint(v, 10, 64); err != nil {
				return
			}
		case "outacc":
			if t.OutAcc, err = strconv.ParseUint(v, 10, 64); err != nil {
				return
			}
		case "script":
			t.Script = []byte(v)
		case "owner":
			if t.PKHash, err = NewPKHashFromAddress(v); err != nil {
				return
			}
		case "spend":
			spend := ByteString([]byte(v))
			t.Spend = &spend
		case "spendHeight":
			var spendHeight uint64
			if spendHeight, err = strconv.ParseUint(v, 10, 32); err != nil {
				return
			}
			t.SpendHeight = uint32(spendHeight)
		}
	}
	return
}

func (t *Txo) AddData(key string, value IIndexable) {
	if t.Data == nil {
		t.Data = map[string]IIndexable{}
	}
	t.Data[key] = value
}

func (t *Txo) AddLog(logName string, logValues map[string]string) {
	if t.logs == nil {
		t.logs = make(map[string]map[string]string)
	}
	log := t.logs[logName]
	if log == nil {
		log = make(map[string]string)
		t.logs[logName] = log
	}
	for k, v := range logValues {
		log[k] = v
	}
}

func (t *Txo) SetSpend(txCtx *IndexContext, cmdable redis.Cmdable, spentScore float64) {
	if err := cmdable.JSONSet(ctx, t.ID(), "$.spend", t.Spend).Err(); err != nil {
		log.Panic(err)
	} else if err := cmdable.JSONSet(ctx, t.ID(), "$.spendHeight", t.SpendHeight).Err(); err != nil {
		log.Panic(err)
	} else if err := cmdable.ZAdd(ctx, "txi:"+txCtx.Txid.String(), redis.Z{
		Score:  float64(t.Vin),
		Member: t.Outpoint.String(),
	}).Err(); err != nil {
		log.Panic(err)
	}

	for tag, mod := range t.Data {
		mod.SetSpend(txCtx, cmdable, t)
		if txCtx.Height > 0 {
			for logName, logValues := range mod.Logs() {
				t.AddLog(fmt.Sprintf("%s:%s", tag, logName), logValues)
			}
		}
		for idxName, idxValue := range mod.OutputIndex() {
			idxKey := strings.Join([]string{"io", tag, idxName}, ":")
			cmdable.ZAdd(ctx, idxKey, redis.Z{
				Score:  spentScore,
				Member: idxValue,
			})
		}
	}
	if err := cmdable.ZAdd(ctx, "txo:state", redis.Z{
		Score:  spentScore,
		Member: t.Outpoint.String(),
	}).Err(); err != nil {
		panic(err)
	}
	// if Rdb != nil {
	// 	Rdb.Publish(context.Background(), hex.EncodeToString(*txo.PKHash), txo.Outpoint.String())
	// }
}

func (t *Txo) Save(txCtx *IndexContext, cmdable redis.Cmdable, spentScore float64) {
	key := t.ID()
	if exists, err := Rdb.Exists(ctx, key, key).Result(); err != nil {
		panic(err)
	} else if exists == 0 {
		if err = cmdable.JSONSet(ctx, key, "$", t).Err(); err != nil {
			panic(err)
		}
	} else {
		if t.Height > 0 {
			if err := cmdable.JSONSet(ctx, key, "height", t.Height).Err(); err != nil {
				panic(err)
			} else if err := cmdable.JSONSet(ctx, key, "idx", t.Idx).Err(); err != nil {
				panic(err)
			}
		}
	}
	for tag, mod := range t.Data {
		mod.Save(txCtx, cmdable, t)
		for idxName, idxValue := range mod.OutputIndex() {
			idxKey := strings.Join([]string{"io", tag, idxName}, ":")
			cmdable.ZAdd(ctx, idxKey, redis.Z{
				Score:  spentScore,
				Member: idxValue,
			})
		}
	}
	if err := cmdable.ZAdd(ctx, "txo:state", redis.Z{
		Score:  spentScore,
		Member: t.Outpoint.String(),
	}).Err(); err != nil {
		panic(err)
	}

	// if Rdb != nil {
	// 	Rdb.Publish(context.Background(), hex.EncodeToString(*txo.PKHash), txo.Outpoint.String())
	// }
}

func LoadTxo(outpoint string) (txo *Txo, err error) {
	if j, err := Rdb.JSONGet(ctx, "txo:"+outpoint).Result(); err == redis.Nil {
		return nil, nil
	} else if err != nil {
		return nil, err
	} else {
		txo := &Txo{}
		err := json.Unmarshal([]byte(j), txo)
		return txo, err
	}
}

func LoadTxos(outpoints []string) ([]*Txo, error) {
	items := make([]*Txo, len(outpoints))
	for i, outpoint := range outpoints {
		if item, err := LoadTxo(outpoint); err != nil {
			return nil, err
		} else {
			items[i] = item
		}
	}

	return items, nil
}

// func (t *Txo) Save() {
// 	var err error
// 	for i := 0; i < 3; i++ {
// 		_, err = Db.Exec(context.Background(), `
// 			INSERT INTO txos(outpoint, satoshis, outacc, pkhash, origin, height, idx, data)
// 			VALUES($1, $2, $3, $4, $5, $6, $7, $8)
// 			ON CONFLICT(outpoint) DO UPDATE SET
// 				satoshis=EXCLUDED.satoshis,
// 				outacc=EXCLUDED.outacc,
// 				pkhash=CASE WHEN EXCLUDED.pkhash IS NULL THEN txos.pkhash ELSE EXCLUDED.pkhash END,
// 				origin=CASE WHEN EXCLUDED.origin IS NULL THEN txos.origin ELSE EXCLUDED.origin END,
// 				height=CASE WHEN EXCLUDED.height IS NULL THEN txos.height ELSE EXCLUDED.height END,
// 				idx=CASE WHEN EXCLUDED.height IS NULL THEN txos.idx ELSE EXCLUDED.idx END,
// 				data=CASE WHEN txos.data IS NULL
// 					THEN EXCLUDED.data
// 					ELSE CASE WHEN EXCLUDED.data IS NULL THEN txos.data ELSE txos.data || EXCLUDED.data END
// 				END`,
// 			t.Outpoint,
// 			t.Satoshis,
// 			t.OutAcc,
// 			t.PKHash,
// 			t.Origin,
// 			t.Height,
// 			t.Idx,
// 			t.Data,
// 		)

// 		if err != nil {
// 			var pgErr *pgconn.PgError
// 			if errors.As(err, &pgErr) {
// 				if pgErr.Code == "23505" {
// 					time.Sleep(100 * time.Millisecond)
// 					// log.Printf("Conflict. Retrying Save %s\n", t.Outpoint)
// 					continue
// 				}
// 				// if pgErr.Code == "22P05" {
// 				// 	delete(t.Data, "insc")
// 				// 	continue
// 				// }
// 			}
// 			log.Panicf("insTxo Err: %s - %v", t.Outpoint, err)
// 		}
// 		break
// 	}
// 	if err != nil {
// 		log.Panicln("insTxo Err:", err)
// 	}
// }

// func (t *Txo) SaveSpend() {
// 	var err error
// 	for i := 0; i < 3; i++ {
// 		_, err = Db.Exec(context.Background(), `
// 			INSERT INTO txos(outpoint, spend, vin, spend_height, spend_idx)
// 			VALUES($1, $2, $3, $4, $5)
// 			ON CONFLICT(outpoint) DO UPDATE SET
// 				spend=EXCLUDED.spend,
// 				vin=EXCLUDED.vin,
// 				spend_height=CASE WHEN EXCLUDED.spend_height IS NULL THEN txos.spend_height ELSE EXCLUDED.spend_height END,
// 				spend_idx=CASE WHEN EXCLUDED.spend_height IS NULL THEN txos.spend_idx ELSE EXCLUDED.spend_idx END`,
// 			t.Outpoint,
// 			t.Spend,
// 			t.Vin,
// 			t.SpendHeight,
// 			t.SpendIdx,
// 		)
// 		if err != nil {
// 			var pgErr *pgconn.PgError
// 			if errors.As(err, &pgErr) {
// 				if pgErr.Code == "23505" {
// 					time.Sleep(100 * time.Millisecond)
// 					// log.Printf("Conflict. Retrying SaveSpend %s\n", t.Outpoint)
// 					continue
// 				}
// 			}
// 			log.Panicln("insTxo Err:", err)
// 		}
// 		break
// 	}
// 	if err != nil {
// 		log.Panicln("insTxo Err:", err)
// 	}
// }

// func (t *Txo) SetOrigin(origin *Outpoint) {
// 	var err error
// 	for i := 0; i < 3; i++ {
// 		_, err = Db.Exec(context.Background(), `
// 			INSERT INTO txos(outpoint, origin, satoshis, outacc)
// 			VALUES($1, $2, $3, $4)
// 			ON CONFLICT(outpoint) DO UPDATE SET
// 				origin=EXCLUDED.origin`,
// 			t.Outpoint,
// 			origin,
// 			t.Satoshis,
// 			t.OutAcc,
// 		)

// 		if err != nil {
// 			var pgErr *pgconn.PgError
// 			if errors.As(err, &pgErr) {
// 				if pgErr.Code == "23505" {
// 					time.Sleep(100 * time.Millisecond)
// 					// log.Printf("Conflict. Retrying SetOrigin %s\n", t.Outpoint)
// 					continue
// 				}
// 			}
// 			log.Panicln("insTxo Err:", err)
// 		}
// 		break
// 	}
// 	if err != nil {
// 		log.Panicln("insTxo Err:", err)
// 	}
// }
