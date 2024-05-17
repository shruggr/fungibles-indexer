package fungibles

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"strconv"

	"github.com/redis/go-redis/v9"
	"github.com/shruggr/fungibles-indexer/lib"
	"github.com/shruggr/fungibles-indexer/mod/ordlock"
)

type FungibleTxo struct {
	lib.Indexable
	Ticker  *string       `json:"tick,omitempty"`
	Id      *lib.Outpoint `json:"id,omitempty"`
	Op      string        `json:"op"`
	Amt     uint64        `json:"amt"`
	Status  int           `json:"status"`
	Reason  *string       `json:"reason,omitempty"`
	Implied *bool         `json:"implied,omitempty"`
	logs    map[string]map[string]string
}

func (t *FungibleTxo) Tag() string {
	return "bsv20"
}

func (t *FungibleTxo) TickID() string {
	if t.Id != nil {
		return t.Id.String()
	} else if t.Ticker != nil {
		return *t.Ticker
	}
	panic("No tick")
}

func (f *FungibleTxo) SetStatus(cmdable redis.Cmdable, txo *lib.Txo, status Bsv20Status, reason string) {
	f.Status = int(status)
	f.Reason = &reason
	if status != 0 {
		cmdable.JSONSet(ctx, txo.ID(), "$.data.bsv20", f)
	}
	f.IndexByScore("stat:"+f.TickID(), txo.Outpoint.String(), float64(status))
	listing := txo.Data["list"].(*ordlock.Listing)
	if listing != nil {
		scoped := uint64(listing.PricePer * math.Pow10(8))
		if scoped > 9999999999999999 {
			scoped = 9999999999999999
		}
		if score, err := strconv.ParseFloat(fmt.Sprintf("%d.%016d", status, scoped), 64); err != nil {
			panic(err)
		} else {
			f.IndexByScore("list:"+f.TickID(), txo.Outpoint.String(), score)
		}
	}
}

func (f *FungibleTxo) SetSpend(txCtx *lib.IndexContext, cmdable redis.Cmdable, txo *lib.Txo) {
	listing := txo.Data["list"].(*ordlock.Listing)
	if listing != nil {
		log := map[string]string{}
		if listing.Sale {
			log["sale"] = txo.Outpoint.String()
		} else {
			log["cancel"] = txo.Outpoint.String()
		}
		f.AddLog("mkt:"+f.TickID(), log)
	}

	if txo.PKHash != nil {
		add, err := txo.PKHash.Address()
		if err != nil {
			panic(err)
		}
		if listing != nil {
			log := map[string]string{}
			if listing.Sale {
				log["sale"] = txo.Outpoint.String()
			} else {
				log["cancel"] = txo.Outpoint.String()
			}
			f.AddLog("mka:"+add, log)
		}
	}

}

func (f *FungibleTxo) Save(txCtx *lib.IndexContext, cmdable redis.Cmdable, txo *lib.Txo) {
	status, err := lib.Rdb.ZScore(ctx, "oi:bsv20:stat:"+f.TickID(), txo.Outpoint.String()).Result()
	if err == redis.Nil {
		f.SetStatus(cmdable, txo, Bsv20Status(f.Status), "")
	} else if err != nil {
		panic(err)
	}

	listing := txo.Data["list"].(*ordlock.Listing)
	if listing != nil {
		f.AddLog("mkt:"+f.TickID(), map[string]string{
			"listing": txo.Outpoint.String(),
		})
	}

	if txo.PKHash != nil {
		add, err := txo.PKHash.Address()
		if err != nil {
			panic(err)
		}
		if listing != nil {
			f.AddLog("mka:"+add, map[string]string{
				"listing": txo.Outpoint.String(),
			})
		}

		f.IndexBySpent(fmt.Sprintf("a:%s:%s", add, f.TickID()), txo.Outpoint.String())
		f.IndexByScore("hold"+f.TickID(), add, 0)
	}

	if txCtx.Height > 0 {
		validateKey := fmt.Sprintf("f:validate:%s:%07d", f.TickID(), txCtx.Height)
		if f.Status == int(Pending) && status == float64(Pending) {
			if err = cmdable.ZAdd(ctx, validateKey, redis.Z{
				Score:  float64(txo.Idx),
				Member: txo.Outpoint.String(),
			}).Err(); err != nil {
				panic(err)
			}
			cmdable.ZAdd(ctx, "stat:"+f.TickID(), redis.Z{
				Score:  float64(Pending),
				Member: txo.Outpoint.String(),
			})
		}
	}

	f.IndexBySpent(f.TickID(), txo.Outpoint.String())
}

func ValidateV2Transfer(txid lib.ByteString, tickId string, isMempool bool) (outputs int, aborted bool) {
	log.Printf("Validating V2 Transfer %x %s\n", txid, tickId)
	inputs, err := lib.Rdb.ZRange(ctx, fmt.Sprintf("txi:%s", txid.String()), 0, -1).Result()
	if err != nil {
		log.Panic(err)
	}
	var reason string
	var tokensIn uint64
	var tokenOuts []uint32
	inTxos, err := lib.LoadTxos(inputs)
	if err != nil {
		panic(err)
	}
	for _, inTxo := range inTxos {
		ftxo := inTxo.Data["bsv20"].(*FungibleTxo)
		if ftxo == nil || ftxo.TickID() != tickId {
			continue
		}
		switch ftxo.Status {
		case -1:
			reason = "invalid input"
		case 0:
			fmt.Printf("inputs pending %s %x\n", tickId, txid)
			return 0, true
		case 1:
			tokensIn += ftxo.Amt
		}
	}

	outpoints, err := lib.Rdb.ZRangeByLex(ctx, "oi:bsv20:"+tickId, &redis.ZRangeBy{
		Min: txid.String(),
		Max: fmt.Sprintf("%s_a", txid.String()),
	}).Result()
	if err != nil {
		log.Panic(err)
	}
	outTxos, err := lib.LoadTxos(outpoints)
	if err != nil {
		panic(err)
	}
	for _, outTxo := range outTxos {
		if fTxo, ok := outTxo.Data["bsv20"].(*FungibleTxo); !ok {
			continue
		} else if reason != "" {
			fmt.Println("Failed:", reason)
			continue
		} else if fTxo.Amt > tokensIn {
			reason = fmt.Sprintf("insufficient balance %d < %d", tokensIn, fTxo.Amt)
			if isMempool {
				fmt.Printf("%s %s - %x\n", tickId, reason, txid)
				return 0, true
			}
		} else {
			tokensIn -= fTxo.Amt
		}
	}

	if _, err := lib.Rdb.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		if reason != "" {
			log.Printf("Transfer Invalid: %x %s %s\n", txid, tickId, reason)
			for _, outTxo := range outTxos {
				fTxo := outTxo.Data["bsv20"].(*FungibleTxo)
				fTxo.SetStatus(pipe, outTxo, Invalid, reason)
			}
		} else {
			log.Printf("Transfer Valid: %x %s\n", txid, tickId)
			for _, outTxo := range outTxos {
				fTxo := outTxo.Data["bsv20"].(*FungibleTxo)
				fTxo.SetStatus(pipe, outTxo, Valid, "")
				if outTxo.Data["list"] != nil {
					out, err := json.Marshal(outTxo)
					if err != nil {
						log.Panic(err)
					}
					log.Println("Publishing", string(out))
					lib.Rdb.Publish(context.Background(), "bsv20listings", out)
				}
			}
		}
		return nil
	}); err != nil {
		log.Panic(err)
	}

	return len(tokenOuts), false
}
