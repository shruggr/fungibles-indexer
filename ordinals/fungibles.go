package ordinals

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"

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

func (b *Fungible) ID() string {
	return "FUNGIBLE:" + b.TickID()
}

func (b *Fungible) TickID() string {
	if b.Id != nil {
		return b.Id.String()
	} else if b.Ticker != nil {
		return *b.Ticker
	}
	panic("No tick")
}

func FormatTickID(tickId string) string {
	if len(tickId) >= 66 {
		return strings.ToLower(tickId)
	} else {
		return strings.ToUpper(tickId)
	}
}

func (b *Fungible) DecrementSupply(cmdable redis.Cmdable, amt uint64) uint64 {
	cmdable.ZIncrBy(ctx, "FSUPPLY", float64(amt)*-1, b.TickID()).Result()
	return b.Max
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
	spends := make([]string, len(txn.Spends))
	sales := make([]bool, len(txn.Spends))
	for vin, spend := range txn.Spends {
		spends[vin] = spend.Outpoint.String()
		sales[vin] = bytes.Contains(*txn.Tx.Inputs[vin].UnlockingScript, ordlock.OrdLockSuffix)
	}
	inTxos, err := LoadFungibleTxos(spends)
	if err != nil {
		panic(err)
	}
	// pipe := lib.Rdb.Pipeline()
	for vin, btxo := range inTxos {
		if btxo == nil {
			continue
		}
		btxo.SetSpend(lib.Rdb, txn.Txid, txn.Height, sales[vin])
		if btxo.PKHash == nil {
			continue
		}
	}

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
			b.Satoshis = txo.Satoshis
			b.Script = txo.Script

			b.Outpoint = *txo.Outpoint
			b.Listing = ordlock.ParseScript(txo)
			if b.Listing != nil {
				f, err := LoadFungible(b.TickID())
				if err == nil && f != nil {
					b.Listing.PricePer = float64(b.Listing.Price) / (float64(b.Amt) / math.Pow(10, float64(f.Decimals)))
				}
			}
			b.PKHash = txo.PKHash
			b.Save(lib.Rdb)
		}
	}
	if err := lib.Rdb.ZAdd(ctx, "TXLOG", redis.Z{Score: float64(txn.Height), Member: txn.Txid.String()}).Err(); err != nil {
		panic(err)
	}
	// _, err = pipe.Exec(ctx)
	// if err != nil {
	// 	panic(err)
	// }
}

var fungCache = make(map[string]*Fungible)

func LoadFungible(tickId string) (ftxo *Fungible, err error) {
	if ft, ok := fungCache[tickId]; ok {
		return ft, nil
	}
	if j, err := lib.Rdb.JSONGet(ctx, "FUNGIBLE:"+tickId).Result(); err != nil {
		return nil, err
	} else {
		ftxo = &Fungible{}
		err = json.Unmarshal([]byte(j), ftxo)
	}
	if err == nil {
		fungCache[tickId] = ftxo
	}
	return
}

func LoadFungibles(tickIds []string) ([]*Fungible, error) {
	keys := make([]string, len(tickIds))
	for i, tickId := range tickIds {
		keys[i] = "FUNGIBLE:" + tickId
	}
	items, err := lib.Rdb.JSONMGet(ctx, "$", keys...).Result()
	if err != nil {
		panic(err)
	}
	tokens := make([]*Fungible, len(tickIds))
	for i, item := range items {
		if item == nil {
			continue
		}
		token := []Fungible{}
		if err := json.Unmarshal([]byte(item.(string)), &token); err != nil {
			panic(err)
		}
		tokens[i] = &token[0]
	}

	return tokens, nil
}
