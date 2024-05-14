package ordinals

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/shruggr/fungibles-indexer/lib"
	"github.com/shruggr/fungibles-indexer/ordlock"
)

type FungibleTxo struct {
	Height   uint32       `json:"height"`
	Idx      uint64       `json:"idx"`
	Outpoint lib.Outpoint `json:"outpoint,omitempty"`
	// Satoshis    uint64           `json:"satoshis,omitempty"`
	// Script      []byte           `json:"script,omitempty"`
	Ticker      *string          `json:"tick,omitempty"`
	Id          *lib.Outpoint    `json:"id,omitempty"`
	Op          string           `json:"op"`
	Amt         uint64           `json:"amt,omitempty"`
	PKHash      *lib.PKHash      `json:"owner,omitempty"`
	Spend       *lib.ByteString  `json:"spend,omitempty"`
	SpendHeight uint32           `json:"spendHeight,omitempty"`
	Listing     *ordlock.Listing `json:"listing"`
	Status      int              `json:"status"`
	Reason      *string          `json:"reason,omitempty"`
	Implied     *bool            `json:"implied,omitempty"`
}

func (b *FungibleTxo) TickID() string {
	if b.Id != nil {
		return b.Id.String()
	} else if b.Ticker != nil {
		return *b.Ticker
	}
	panic("No tick")
}

func (b *FungibleTxo) Save() {
	if err := lib.Rdb.Watch(ctx, func(tx *redis.Tx) error {
		var status int
		key := "FTXO:" + b.Outpoint.String()
		if statusStr, err := tx.JSONGet(ctx, key, "$.status").Result(); err == redis.Nil {
			if err = tx.JSONSet(ctx, key, "$", b).Err(); err != nil {
				panic(err)
			}
		} else if err != nil {
			panic(err)
		} else {
			if status, err = strconv.Atoi(statusStr); err != nil {
				panic(err)
			}
			update := map[string]interface{}{
				"status": b.Status,
			}
			if b.Reason != nil {
				update["reason"] = *b.Reason
			}
			if b.Height > 0 {
				update["height"] = b.Height
				update["idx"] = b.Idx
			}
			if b.Spend != nil {
				update["spend"] = *b.Spend
				update["spendHeight"] = b.SpendHeight
			}
			if b.Listing != nil {
				update["listing"] = *b.Listing
			}
			j, err := json.Marshal(update)
			if err = tx.JSONMerge(ctx, key, "$", string(j)).Err(); err != nil {
				panic(err)
			}
		}

		var spent uint8
		if b.Spend != nil {
			tx.SAdd(ctx, fmt.Sprintf("FTXI:%s:%s", b.Spend.String(), b.TickID()), b.Outpoint.String())
			spent = 1
		}
		height := b.Height
		if height == 0 {
			height = uint32(time.Now().Unix())
		}
		spentScore, err := strconv.ParseFloat(fmt.Sprintf("%d.%010d", spent, height), 64)
		if err != nil {
			panic(err)
		}

		if b.PKHash != nil {
			add, err := b.PKHash.Address()
			if err != nil {
				panic(err)
			}

			if b.Spend != nil {
				if err = tx.ZAdd(ctx, fmt.Sprintf("FADDSPND:%s:%s", add, b.TickID()), redis.Z{
					Score:  float64(b.SpendHeight),
					Member: b.Outpoint.String(),
				}).Err(); err != nil {
					panic(err)
				}
			}

			if err = tx.ZAdd(ctx, fmt.Sprintf("FADDTXO:%s:%s", add, b.TickID()), redis.Z{
				Score:  spentScore,
				Member: b.Outpoint.String(),
			}).Err(); err != nil {
				panic(err)
			}
		}

		if b.Listing != nil {
			scoped := uint64(b.Listing.PricePer * math.Pow10(8))
			if scoped > 9999999999999999 {
				scoped = 9999999999999999
			}
			if score, err := strconv.ParseFloat(fmt.Sprintf("%d.%016d", spent, scoped), 64); err != nil {
				panic(err)
			} else if err = tx.ZAdd(ctx, "FLIST:"+b.TickID(), redis.Z{
				Score:  score,
				Member: b.Outpoint.String(),
			}).Err(); err != nil {
				panic(err)
			}

			if b.Listing.Sale {
				score := float64(b.SpendHeight)
				if score == 0 {
					score = float64(time.Now().Unix())
				}
				if err = tx.ZAdd(ctx, "FSALE:"+b.TickID(), redis.Z{
					Score:  score,
					Member: b.Outpoint.String(),
				}).Err(); err != nil {
					panic(err)
				}
			}
		}

		if b.Height > 0 {
			validateKey := fmt.Sprintf("FVALIDATE:%s:%07d", b.TickID(), b.Height)
			if b.Status == int(Pending) && status == int(Pending) {
				if err = tx.ZAdd(ctx, validateKey, redis.Z{
					Score:  float64(b.Idx),
					Member: b.Outpoint.String(),
				}).Err(); err != nil {
					panic(err)
				}
			} else {
				if err = tx.ZRem(ctx, validateKey, b.Outpoint.String()).Err(); err != nil {
					panic(err)
				}
			}
		}
		if err = tx.ZAdd(ctx, "TXOSTATE", redis.Z{
			Score:  spentScore,
			Member: b.Outpoint.String(),
		}).Err(); err != nil {
			panic(err)
		}
		if err = tx.ZAdd(ctx, "FTXOSTATE:"+b.TickID(), redis.Z{
			Score:  spentScore,
			Member: b.Outpoint.String(),
		}).Err(); err != nil {
			panic(err)
		}
		return nil
	}); err != nil {
		panic(err)
	}
}

func ValidateV2Transfer(txid lib.ByteString, tickId string, isMempool bool) (outputs int) {
	log.Printf("Validating V2 Transfer %x %s\n", txid, tickId)
	inputs, err := lib.Rdb.ZRange(ctx, fmt.Sprintf("FTXI:%s:%s", txid.String(), tickId), 0, -1).Result()
	if err != nil {
		log.Panic(err)
	}
	var reason string
	var tokensIn uint64
	var tokenOuts []uint32
	for _, outpoint := range inputs {
		inJson, err := lib.Rdb.JSONGet(ctx, "$", "FTXO:"+outpoint).Result()
		if err != nil {
			log.Panic(err)
		}
		inTxo := FungibleTxo{}
		if err := json.Unmarshal([]byte(inJson), &inTxo); err != nil {
			log.Panic(err)
		}

		switch inTxo.Status {
		case -1:
			reason = "invalid input"
		case 0:
			fmt.Printf("inputs pending %s %x\n", tickId, txid)
			return
		case 1:
			tokensIn += inTxo.Amt
		}
	}

	outpoints, err := lib.Rdb.ZRangeByLex(ctx, "FTXOSTATE:"+tickId, &redis.ZRangeBy{
		Min: txid.String(),
		Max: fmt.Sprintf("%s_a", txid.String()),
	}).Result()
	if err != nil {
		log.Panic(err)
	}
	outTxos := make([]*FungibleTxo, len(outpoints))
	for i, outpoint := range outpoints {
		outJson, err := lib.Rdb.JSONGet(ctx, "$", "FTXO:"+outpoint).Result()
		if err != nil {
			log.Panic(err)
		}
		outTxo := FungibleTxo{}
		if err := json.Unmarshal([]byte(outJson), &outTxo); err != nil {
			log.Panic(err)
		}
		outTxos[i] = &outTxo
		// tokenOuts = append(tokenOuts, outTxo.Outpoint.Vout())
		if reason != "" {
			fmt.Println("Failed:", reason)
			continue
		}
		if outTxo.Amt > tokensIn {
			reason = fmt.Sprintf("insufficient balance %d < %d", tokensIn, outTxo.Amt)
			if isMempool {
				fmt.Printf("%s %s - %x\n", tickId, reason, txid)
				return
			}
		} else {
			tokensIn -= outTxo.Amt
		}

	}

	if _, err := lib.Rdb.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		if reason != "" {
			log.Printf("Transfer Invalid: %x %s %s\n", txid, tickId, reason)
			update := map[string]interface{}{
				"status": -1,
				"reason": reason,
			}
			m, err := json.Marshal(update)
			if err != nil {
				log.Panic(err)
			}
			for _, outpoint := range outpoints {
				pipe.JSONMerge(ctx, "FTXO:"+outpoint, "$", string(m))
			}
		} else {
			log.Printf("Transfer Valid: %x %s\n", txid, tickId)
			update := map[string]interface{}{
				"status": 1,
			}
			m, err := json.Marshal(update)
			if err != nil {
				log.Panic(err)
			}

			for _, outTxo := range outTxos {
				pipe.JSONMerge(ctx, "FTXO:"+outTxo.Outpoint.String(), "$", string(m))

				log.Printf("Validating %s %s\n", tickId, outTxo.Outpoint.String())
				if outTxo.Listing != nil {
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

	return len(tokenOuts)
}
