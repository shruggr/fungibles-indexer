package fungibles

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"

	"github.com/redis/go-redis/v9"
	"github.com/shruggr/fungibles-indexer/lib"
	"github.com/shruggr/fungibles-indexer/mod/ord"
	"github.com/shruggr/fungibles-indexer/mod/ordlock"
)

type Token struct {
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

func (b *Token) ID() string {
	return "f:token:" + b.TickID()
}

func (b *Token) TickID() string {
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

func (b *Token) DecrementSupply(cmdable redis.Cmdable, amt uint64) uint64 {
	cmdable.ZIncrBy(ctx, "f:supply", float64(amt)*-1, b.TickID()).Result()
	return b.Max
}

func (b *Token) Save() {
	// Don't save if v1 and unmined
	if b.Height == 0 && b.Id == nil {
		return
	}

	key := b.ID()
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
			tx.ZAdd(ctx, "f:supply", redis.Z{Score: float64(b.Max), Member: b.TickID()})
		}
		return nil
	}, key)
}

func IndexFungibles(txn *lib.IndexContext) {
	ord.ParseInscriptions(txn)
	spends := make([]string, len(txn.Spends))
	sales := make([]bool, len(txn.Spends))
	for vin, spend := range txn.Spends {
		spends[vin] = spend.Outpoint.String()
		sales[vin] = bytes.Contains(*txn.Tx.Inputs[vin].UnlockingScript, ordlock.OrdLockSuffix)
	}
	inTxos, err := lib.LoadTxos(spends)
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

		if b, ok := bsv20.(Token); ok {

			b.Height = txn.Height
			b.Idx = txn.Idx
			b.Outpoint = txo.Outpoint
			b.Save()
			if b.Op == "deploy+mint" {
				bsv20 = FungibleTxo{

					Id:  b.Id,
					Op:  "deploy+mint",
					Amt: b.Max,
					// PKHash: txo.PKHash,
				}
			}
		}

		if b, ok := bsv20.(FungibleTxo); ok {
			b.Height = txn.Height
			b.Idx = txn.Idx
			b.Satoshis = txo.Satoshis
			b.Script = txo.Script

			b.Outpoint = txo.Outpoint
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
	if err := lib.Rdb.ZAdd(ctx, "tx:log", redis.Z{Score: float64(txn.Height), Member: txn.Txid.String()}).Err(); err != nil {
		panic(err)
	}
	// _, err = pipe.Exec(ctx)
	// if err != nil {
	// 	panic(err)
	// }
}

var fungCache = make(map[string]*Token)

func LoadFungible(tickId string) (ftxo *Token, err error) {
	if ft, ok := fungCache[tickId]; ok {
		return ft, nil
	}
	if j, err := lib.Rdb.JSONGet(ctx, "f:token::"+tickId).Result(); err != nil {
		return nil, err
	} else {
		ftxo = &Token{}
		err = json.Unmarshal([]byte(j), ftxo)
	}
	if err == nil {
		fungCache[tickId] = ftxo
	}
	return
}

func LoadFungibles(tickIds []string) ([]*Token, error) {
	keys := make([]string, len(tickIds))
	for i, tickId := range tickIds {
		keys[i] = "f:token:" + tickId
	}
	items, err := lib.Rdb.JSONMGet(ctx, "$", keys...).Result()
	if err != nil {
		panic(err)
	}
	tokens := make([]*Token, len(tickIds))
	for i, item := range items {
		if item == nil {
			continue
		}
		token := []Token{}
		if err := json.Unmarshal([]byte(item.(string)), &token); err != nil {
			panic(err)
		}
		tokens[i] = &token[0]
	}

	return tokens, nil
}
