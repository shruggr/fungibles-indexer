package ordinals

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"

	"github.com/redis/go-redis/v9"
	"github.com/shruggr/fungibles-indexer/lib"
	"github.com/shruggr/fungibles-indexer/ordlock"
)

type Fungible struct {
	Height     uint32        `json:"height,omitempty"`
	Idx        uint64        `json:"idx,omitempty"`
	Outpoint   *lib.Outpoint `json:"outpoint,omitempty"`
	Ticker     *string       `json:"tick,omitempty"`
	Id         *lib.Outpoint `json:"id,omitempty"`
	Op         string        `json:"op"`
	Max        uint64        `json:"max,omitempty"`
	Limit      *uint64       `json:"lim,omitempty"`
	Decimals   uint8         `json:"dec,omitempty"`
	Symbol     *string       `json:"sym,omitempty"`
	Icon       *lib.Outpoint `json:"icon,omitempty"`
	Contract   *string       `json:"contract,omitempty"`
	FundPath   string        `json:"fundPath,omitempty"`
	FundPKHash lib.PKHash    `json:"fundPKHash,omitempty"`
	Output     []byte        `json:"-"`
}

func (b *Fungible) TickID() string {
	if b.Id != nil {
		return b.Id.String()
	} else if b.Ticker != nil {
		return *b.Ticker
	}
	panic("No tick")
}

func (b *Fungible) Save() {
	// Don't save if v1 and unmined
	if b.Height == 0 && b.Id == nil {
		return
	}

	key := "FUNGIBLE:" + b.TickID()
	lib.Rdb.Watch(ctx, func(tx *redis.Tx) error {
		if exists, err := tx.Exists(ctx, key).Result(); err != nil {
			panic(err)
		} else if exists == 1 {
			return nil
		}

		if err := tx.JSONSet(ctx, key, "$", b).Err(); err != nil {
			panic(err)
		}
		if b.Id == nil {
			tx.ZAdd(ctx, "FSUPPLY", redis.Z{Score: float64(b.Max), Member: b.TickID()})
		}
		return nil
	})
}

func IndexFungibles(txn *lib.IndexContext) {
	ParseInscriptions(txn)
	IndexFungibleSpends(txn)
	IndexFungibleTxos(txn)
	if err := lib.Rdb.ZAdd(ctx, "TXLOG", redis.Z{Score: float64(txn.Height), Member: txn.Txid}).Err(); err != nil {
		panic(err)
	}
}

func IndexFungibleSpends(txn *lib.IndexContext) {
	spends := make([]string, len(txn.Spends))
	sales := make([]bool, len(txn.Spends))
	for vin, spend := range txn.Spends {
		spends[vin] = spend.Outpoint.String()
		sales[vin] = bytes.Contains(*txn.Tx.Inputs[vin].UnlockingScript, ordlock.OrdLockSuffix)
	}
	ftxos, err := LoadFungibleTxos(spends, false)
	if err != nil {
		panic(err)
	}

	pipe := lib.Rdb.Pipeline()
	// spent := make([]string, len(txn.Spends))
	for vin, btxo := range ftxos {
		if btxo == nil {
			continue
		}

		btxo.Spend = &txn.Txid
		btxo.SpendHeight = txn.Height
		if btxo.Listing != nil {
			btxo.Listing.Sale = sales[vin]
		}
		btxo.Save()
		if btxo.PKHash == nil {
			continue
		}
	}

	_, err = pipe.Exec(ctx)
	if err != nil {
		panic(err)
	}
}

func IndexFungibleTxos(txn *lib.IndexContext) {
	for _, txo := range txn.Txos {
		bsv20, ok := txo.Data["bsv20"]
		if !ok {
			continue
		}

		if b, ok := bsv20.(Fungible); ok {
			b.Height = txn.Height
			b.Idx = txn.Idx
			b.Outpoint = txo.Outpoint
			b.Save()
			if b.Op == "deploy+mint" {
				bsv20 = FungibleTxo{
					Id:     b.Id,
					Op:     "deploy+mint",
					Amt:    b.Max,
					PKHash: txo.PKHash,
				}
			}
		}

		if b, ok := bsv20.(FungibleTxo); ok {
			b.Height = txn.Height
			b.Idx = txn.Idx

			b.Outpoint = *txo.Outpoint
			b.Listing = ordlock.ParseScript(txo)
			if b.Listing != nil {
				f, err := LoadFungible(b.TickID(), false)
				if err == nil && f != nil {
					b.Listing.PricePer = float64(b.Listing.Price) / (float64(b.Amt) / math.Pow(10, float64(f.Decimals)))
				}
			}
			b.PKHash = txo.PKHash
			b.Save()
		}
	}
}

var fungCache = make(map[string]*Fungible)

func LoadFungible(tickId string, includeMempool bool) (*Fungible, error) {
	if ft, ok := fungCache[tickId]; ok {
		return ft, nil
	}
	j, err := lib.Rdb.JSONGet(ctx, "FUNGIBLE:"+tickId).Result()
	if err == redis.Nil && includeMempool {
		j, err = lib.Rdb.JSONGet(ctx, "MFUNGIBLE:"+tickId).Result()
	}
	if err != nil {
		return nil, err
	}
	ftxo := &Fungible{}
	err = json.Unmarshal([]byte(j), ftxo)
	if err == nil {
		fungCache[tickId] = ftxo
	}
	return ftxo, err
}

func LoadFungibleTxo(outpoint lib.Outpoint, includeMempool bool) (ftxo *FungibleTxo, err error) {
	j, err := lib.Rdb.JSONGet(ctx, fmt.Sprintf("FTXO:%s", outpoint.String())).Result()
	if err == redis.Nil && includeMempool {
		j, err = lib.Rdb.JSONGet(ctx, fmt.Sprintf("MFTXO:%s", outpoint.String())).Result()
	}
	if err != nil {
		return
	}
	ftxo = &FungibleTxo{}
	err = json.Unmarshal([]byte(j), ftxo)
	return
}

func LoadFungibleTxos(outpoints []string, includeMempool bool) ([]*FungibleTxo, error) {
	keys := make([]string, len(outpoints))
	for i, outpoint := range outpoints {
		keys[i] = fmt.Sprintf("FTXO:%s", outpoint)
	}
	items, err := lib.Rdb.JSONMGet(ctx, "$", keys...).Result()
	if err != nil {
		panic(err)
	}
	ftxos := make([]*FungibleTxo, len(outpoints))
	for i, item := range items {
		btxo := []FungibleTxo{}
		if item == nil {
			if !includeMempool {
				continue
			}
			item, err = lib.Rdb.JSONGet(ctx, fmt.Sprintf("MFTXO:%s", outpoints[i])).Result()
			if err == redis.Nil {
				continue
			} else if err != nil {
				panic(err)
			}
		}
		if err := json.Unmarshal([]byte(item.(string)), &btxo); err != nil {
			panic(err)
		}
		ftxos[i] = &btxo[0]
	}

	return ftxos, nil
}
