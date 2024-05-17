package lib

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

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
	InAcc       uint64                       `json:"inacc"`
	SpendHeight uint32                       `json:"spend_height"`
	SpendIdx    uint64                       `json:"spend_idx"`
	Data        map[string]IIndexable        `json:"data,omitempty"`
	logs        map[string]map[string]string `json:"-"`
	// Tx   *bt.Tx                       `json:"-"`
}

func (t *Txo) ID() string {
	return "txo:" + t.Outpoint.String()
}

func (t *Txo) AddData(key string, value IIndexable) {
	if t.Data == nil {
		t.Data = map[string]IIndexable{}
	}
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
	update := map[string]interface{}{
		"spend":       t.Spend,
		"spendHeight": t.SpendHeight,
		"spendIdx":    t.SpendIdx,
		"vin":         t.Vin,
		"inacc":       t.InAcc,
	}
	if j, err := json.Marshal(update); err != nil {
		log.Panic(err)
	} else if err := cmdable.JSONMerge(ctx, t.ID(), "$", string(j)).Err(); err != nil {
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

func (t *Txo) Save(txCtx *IndexContext, cmdable redis.Cmdable) {
	scoreHeight := txCtx.Height
	if scoreHeight == 0 {
		scoreHeight = uint32(time.Now().Unix())
	}
	spent := 0
	if t.Spend != nil {
		spent = 1
	}
	spentScore, err := strconv.ParseFloat(fmt.Sprintf("%d.%010d", spent, scoreHeight), 64)
	if err != nil {
		panic(err)
	}

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

func RefreshAddress(ctx context.Context, address string) error {
	lastHeight, err := Rdb.HGet(ctx, "add:sync", address).Uint64()
	txns, err := JB.GetAddressTransactions(ctx, address, uint32(lastHeight))
	if err != nil {
		return err
	}

	for _, txn := range txns {
		if _, err := Rdb.ZScore(ctx, "tx:log", txn.ID).Result(); err == nil {
			continue
		} else if err != redis.Nil {
			return err
		}
		if txn.BlockHeight > uint32(lastHeight) {
			lastHeight = uint64(txn.BlockHeight)
		}
		if rawtx, err := JB.GetRawTransaction(ctx, txn.ID); err != nil {
			return err
		} else if _, err := IndexTxn(rawtx, txn.BlockHash, txn.BlockHeight, txn.BlockIndex); err != nil {
			return err
		}
	}
	if lastHeight > 5 {
		lastHeight -= 5
	}
	return Rdb.HSet(ctx, "add:sync", address, lastHeight).Err()
}
