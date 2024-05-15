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
	Height      uint32           `json:"height"`
	Idx         uint64           `json:"idx"`
	Outpoint    lib.Outpoint     `json:"outpoint,omitempty"`
	Satoshis    uint64           `json:"satoshis,omitempty"`
	Script      []byte           `json:"script,omitempty"`
	Ticker      *string          `json:"tick,omitempty"`
	Id          *lib.Outpoint    `json:"id,omitempty"`
	Op          string           `json:"op"`
	Amt         uint64           `json:"amt"`
	PKHash      *lib.PKHash      `json:"owner,omitempty"`
	Spend       *lib.ByteString  `json:"spend,omitempty"`
	SpendHeight uint32           `json:"spendHeight,omitempty"`
	Listing     *ordlock.Listing `json:"listing,omitempty"`
	Status      int              `json:"status"`
	Reason      *string          `json:"reason,omitempty"`
	Implied     *bool            `json:"implied,omitempty"`
}

func (t *FungibleTxo) ID() string {
	return "FTXO:" + t.Outpoint.String()
}

func (t *FungibleTxo) TickID() string {
	if t.Id != nil {
		return t.Id.String()
	} else if t.Ticker != nil {
		return *t.Ticker
	}
	panic("No tick")
}

func (t *FungibleTxo) Map() (m map[string]interface{}, err error) {
	m = map[string]interface{}{
		"height":      t.Height,
		"idx":         t.Idx,
		"outpoint":    t.Outpoint.String(),
		"satoshis":    t.Satoshis,
		"script":      t.Script,
		"op":          t.Op,
		"amt":         t.Amt,
		"spend":       t.Spend,
		"spendHeight": t.SpendHeight,
		"status":      t.Status,
	}
	if t.Ticker != nil {
		m["tick"] = *t.Ticker
	}
	if t.Id != nil {
		m["id"] = t.Id.String()
	}
	if t.PKHash != nil {
		if m["owner"], err = t.PKHash.Address(); err != nil {
			return
		}
	}
	if t.Listing != nil {
		if m["listing"], err = json.Marshal(t.Listing); err != nil {
			return
		}
	}
	if t.Reason != nil {
		m["reason"] = t.Reason
	}
	if t.Implied != nil {
		m["implied"] = t.Implied
	}
	return
}

func NewFungibleTxoFromMap(m map[string]string) (t *FungibleTxo, err error) {
	t = &FungibleTxo{}
	for k, v := range m {
		switch k {
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
		case "outpoint":
			var outpoint *lib.Outpoint
			if outpoint, err = lib.NewOutpointFromString(v); err != nil {
				return
			}
			t.Outpoint = *outpoint
		case "satoshis":
			if t.Satoshis, err = strconv.ParseUint(v, 10, 64); err != nil {
				return
			}
		case "script":
			t.Script = []byte(v)
		case "tick":
			t.Ticker = &v
		case "id":
			var id *lib.Outpoint
			if id, err = lib.NewOutpointFromString(v); err != nil {
				return
			}
			t.Id = id
		case "op":
			t.Op = v
		case "amt":
			if t.Amt, err = strconv.ParseUint(v, 10, 64); err != nil {
				return
			}
		case "owner":
			if t.PKHash, err = lib.NewPKHashFromAddress(v); err != nil {
				return
			}
		case "spend":
			spend := lib.ByteString([]byte(v))
			t.Spend = &spend
		case "spendHeight":
			var spendHeight uint64
			if spendHeight, err = strconv.ParseUint(v, 10, 32); err != nil {
				return
			}
			t.SpendHeight = uint32(spendHeight)
		case "status":
			if t.Status, err = strconv.Atoi(v); err != nil {
				return
			}
		case "listing":
			t.Listing = &ordlock.Listing{}
			if err = json.Unmarshal([]byte(v), t.Listing); err != nil {
				return nil, err
			}
		case "reason":
			t.Reason = &v
		case "implied":
			implied, err := strconv.ParseBool(v)
			if err != nil {
				return nil, err
			}
			t.Implied = &implied

		}
	}
	return
}

func (t *FungibleTxo) SetStatus(cmdable redis.Cmdable, status Bsv20Status, reason string) {
	update := map[string]interface{}{
		"status": status,
	}
	if reason != "" {
		update["reason"] = reason
	}
	cmdable.ZAdd(ctx, "FSTATUS:"+t.TickID(), redis.Z{Score: float64(status), Member: t.Outpoint.String()})
	cmdable.HMSet(ctx, t.ID(), update)
	if t.Listing != nil {
		scoped := uint64(t.Listing.PricePer * math.Pow10(8))
		if scoped > 9999999999999999 {
			scoped = 9999999999999999
		}
		if score, err := strconv.ParseFloat(fmt.Sprintf("%d.%016d", status, scoped), 64); err != nil {
			panic(err)
		} else if err = cmdable.ZAdd(ctx, "FLIST:"+t.TickID(), redis.Z{
			Score:  score,
			Member: t.Outpoint.String(),
		}).Err(); err != nil {
			panic(err)
		}
	}
	t.Status = int(status)
	t.Reason = &reason
}

func (t *FungibleTxo) SetSpend(cmdable redis.Cmdable, spend lib.ByteString, spendHeight uint32, sale bool) {
	t.Spend = &spend
	t.SpendHeight = spendHeight
	update := map[string]interface{}{
		"spend":       spend.String(),
		"spendHeight": spendHeight,
	}
	if err := cmdable.HMSet(ctx, t.ID(), update).Err(); err != nil {
		log.Panic(err)
	} else if err := cmdable.SAdd(ctx, fmt.Sprintf("FTXI:%s:%s", t.Spend.String(), t.TickID()), t.Outpoint.String()).Err(); err != nil {
		log.Panic(err)
	}

	height := spendHeight
	if height == 0 {
		height = uint32(time.Now().Unix())
	}
	spentScore, err := strconv.ParseFloat(fmt.Sprintf("1.%010d", height), 64)
	if err != nil {
		panic(err)
	}

	if t.PKHash != nil {
		if add, err := t.PKHash.Address(); err != nil {
			panic(err)
		} else if err = cmdable.ZAdd(ctx, fmt.Sprintf("FADDTXO:%s:%s", add, t.TickID()), redis.Z{
			Score:  spentScore,
			Member: t.Outpoint.String(),
		}).Err(); err != nil {
			panic(err)
		} else if err = cmdable.ZAdd(ctx, fmt.Sprintf("FADDSPND:%s:%s", add, t.TickID()), redis.Z{
			Score:  float64(t.SpendHeight),
			Member: t.Outpoint.String(),
		}).Err(); err != nil {
			panic(err)
		}
	}

	if t.Listing != nil {
		t.Listing.Sale = sale
		if sale {
			score := float64(t.SpendHeight)
			if score == 0 {
				score = float64(time.Now().Unix())
			}
			if err = cmdable.ZAdd(ctx, "FSALE:"+t.TickID(), redis.Z{
				Score:  score,
				Member: t.Outpoint.String(),
			}).Err(); err != nil {
				panic(err)
			}
		}
	}
	if err = cmdable.ZAdd(ctx, "TXOSTATE", redis.Z{
		Score:  spentScore,
		Member: t.Outpoint.String(),
	}).Err(); err != nil {
		panic(err)
	}
	if err = cmdable.ZAdd(ctx, "FTXOSTATE:"+t.TickID(), redis.Z{
		Score:  spentScore,
		Member: t.Outpoint.String(),
	}).Err(); err != nil {
		panic(err)
	}
}

func (t *FungibleTxo) Save(cmdable redis.Cmdable) {
	key := t.ID()
	status, err := lib.Rdb.HGet(ctx, key, "status").Int()
	if err == redis.Nil {
		j, err := json.Marshal(t)
		if err != nil {
			panic(err)
		}
		m := map[string]interface{}{}
		if err = json.Unmarshal(j, &m); err != nil {
			panic(err)
		} else if err = cmdable.HMSet(ctx, key, m).Err(); err != nil {
			panic(err)
		}
		t.SetStatus(cmdable, Bsv20Status(t.Status), "")
	} else if err != nil {
		panic(err)
	} else {
		if t.Height > 0 {
			update := map[string]interface{}{
				"height": t.Height,
				"idx":    t.Idx,
			}
			if err := cmdable.HMSet(ctx, key, update).Err(); err != nil {
				panic(err)
			}
		}
	}

	height := t.Height
	if height == 0 {
		height = uint32(time.Now().Unix())
	}
	spentScore, err := strconv.ParseFloat(fmt.Sprintf("0.%010d", height), 64)
	if err != nil {
		panic(err)
	}

	if t.PKHash != nil {
		if add, err := t.PKHash.Address(); err != nil {
			panic(err)
		} else if err = cmdable.ZAdd(ctx, fmt.Sprintf("FADDTXO:%s:%s", add, t.TickID()), redis.Z{
			Score:  spentScore,
			Member: t.Outpoint.String(),
		}).Err(); err != nil {
			panic(err)
		} else if err = cmdable.ZAddNX(ctx, "FHOLD:"+t.TickID(), redis.Z{
			Score:  0,
			Member: add,
		}).Err(); err != nil {
			panic(err)
		}
	}

	if t.Height > 0 {
		validateKey := fmt.Sprintf("FVALIDATE:%s:%07d", t.TickID(), t.Height)
		if t.Status == int(Pending) && status == int(Pending) {
			if err = cmdable.ZAdd(ctx, validateKey, redis.Z{
				Score:  float64(t.Idx),
				Member: t.Outpoint.String(),
			}).Err(); err != nil {
				panic(err)
			}
			cmdable.ZAdd(ctx, "FSTATUS:"+t.TickID(), redis.Z{Score: float64(Pending), Member: t.Outpoint.String()})
		}
	}
	if err = cmdable.ZAdd(ctx, "TXOSTATE", redis.Z{
		Score:  spentScore,
		Member: t.Outpoint.String(),
	}).Err(); err != nil {
		panic(err)
	}
	if err = cmdable.ZAdd(ctx, "FTXOSTATE:"+t.TickID(), redis.Z{
		Score:  spentScore,
		Member: t.Outpoint.String(),
	}).Err(); err != nil {
		panic(err)
	}

}

func ValidateV2Transfer(txid lib.ByteString, tickId string, isMempool bool) (outputs int, aborted bool) {
	log.Printf("Validating V2 Transfer %x %s\n", txid, tickId)
	inputs, err := lib.Rdb.ZRange(ctx, fmt.Sprintf("FTXI:%s:%s", txid.String(), tickId), 0, -1).Result()
	if err != nil {
		log.Panic(err)
	}
	var reason string
	var tokensIn uint64
	var tokenOuts []uint32
	inTxos, err := LoadFungibleTxos(inputs)
	if err != nil {
		panic(err)
	}
	for _, inTxo := range inTxos {
		switch inTxo.Status {
		case -1:
			reason = "invalid input"
		case 0:
			fmt.Printf("inputs pending %s %x\n", tickId, txid)
			return 0, true
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
	outTxos, err := LoadFungibleTxos(outpoints)
	if err != nil {
		panic(err)
	}
	for _, outTxo := range outTxos {
		if reason != "" {
			fmt.Println("Failed:", reason)
			continue
		}
		if outTxo.Amt > tokensIn {
			reason = fmt.Sprintf("insufficient balance %d < %d", tokensIn, outTxo.Amt)
			if isMempool {
				fmt.Printf("%s %s - %x\n", tickId, reason, txid)
				return 0, true
			}
		} else {
			tokensIn -= outTxo.Amt
		}
	}

	if _, err := lib.Rdb.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		if reason != "" {
			log.Printf("Transfer Invalid: %x %s %s\n", txid, tickId, reason)
			for _, outTxo := range outTxos {
				outTxo.SetStatus(pipe, Invalid, reason)
			}
		} else {
			log.Printf("Transfer Valid: %x %s\n", txid, tickId)
			for _, outTxo := range outTxos {
				outTxo.SetStatus(pipe, Valid, "")
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

	return len(tokenOuts), false
}

func LoadFungibleTxo(outpoint string) (ftxo *FungibleTxo, err error) {
	if m, err := lib.Rdb.HGetAll(ctx, "FTXO:"+outpoint).Result(); err != nil {
		return nil, err
	} else if len(m) == 0 {
		return nil, nil
	} else if ftxo, err = NewFungibleTxoFromMap(m); err != nil {
		return nil, err
	} else {
		return ftxo, nil
	}
}

func LoadFungibleTxos(outpoints []string) ([]*FungibleTxo, error) {
	items := make([]*FungibleTxo, len(outpoints))
	for i, outpoint := range outpoints {
		if item, err := LoadFungibleTxo(outpoint); err != nil {
			return nil, err
		} else {
			items[i] = item
		}
	}

	return items, nil
}
