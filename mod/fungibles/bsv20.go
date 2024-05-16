package fungibles

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/libsv/go-bk/bip32"
	"github.com/libsv/go-bk/crypto"
	"github.com/libsv/go-bt/bscript"
	"github.com/redis/go-redis/v9"
	"github.com/shruggr/fungibles-indexer/lib"
	"github.com/shruggr/fungibles-indexer/mod/ord"
)

type Bsv20Status int

var hdKey, _ = bip32.NewKeyFromString("xpub661MyMwAqRbcF221R74MPqdipLsgUevAAX4hZP2rywyEeShpbe3v2r9ciAvSGT6FB22TEmFLdUyeEDJL4ekG8s9H5WXbzDQPr6eW1zEYYy9")

const (
	Invalid Bsv20Status = -1
	Pending Bsv20Status = 0
	Valid   Bsv20Status = 1
	MintFee uint64      = 100
)

const FUNGIBLE_OP_COST = 1000
const BSV20_INCLUDE_FEE = 10000000

var ctx = context.Background()

func ParseBsv20Inscription(ord *lib.File, txo *lib.Txo) (interface{}, error) {
	mime := strings.ToLower(ord.Type)
	if !strings.HasPrefix(mime, "application/bsv-20") &&
		!(txo.Height > 0 && txo.Height < 793000 && strings.HasPrefix(mime, "text/plain")) {
		return nil, nil
	}
	data := map[string]string{}
	err := json.Unmarshal(ord.Content, &data)
	if err != nil {
		// fmt.Println("JSON PARSE ERROR:", string(ord.Content), err)
		return nil, nil
	}
	var protocol string
	var ok bool
	if protocol, ok = data["p"]; !ok || protocol != "bsv-20" {
		return nil, nil
	}

	var op string
	if op, ok = data["op"]; !ok {
		return nil, nil
	}

	var tick *string
	var id *lib.Outpoint
	if val, ok := data["id"]; ok {
		id, err = lib.NewOutpointFromString(val)
		if err != nil {
			return nil, nil
		}
	} else if val, ok := data["tick"]; ok {
		val = strings.ToUpper(val)
		chars := []rune(val)
		if len(chars) > 4 {
			return nil, nil
		}
		tick = &val
	} else if op == "deploy+mint" {
		id = txo.Outpoint
	} else {
		return nil, nil
	}

	switch op {
	case "deploy", "deploy+mint":
		f := Token{
			Ticker: tick,
			Id:     id,
			Op:     op,
		}

		if val, ok := data["max"]; ok && f.Op == "deploy" {
			f.Max, err = strconv.ParseUint(val, 10, 64)
			if err != nil || f.Max == 0 {
				return nil, nil
			}
		} else if val, ok := data["amt"]; ok && f.Op == "deploy+mint" {
			f.Max, err = strconv.ParseUint(val, 10, 64)
			if err != nil || f.Max == 0 {
				return nil, nil
			}
		} else {
			return nil, nil
		}

		if val, ok := data["lim"]; ok {
			l, err := strconv.ParseUint(val, 10, 64)
			if err != nil {
				return nil, nil
			}
			f.Limit = &l
		}

		if decimals, ok := data["dec"]; ok {
			val, err := strconv.ParseUint(decimals, 10, 8)
			if err != nil || val > 18 {
				return nil, nil
			}
			f.Decimals = uint8(val)
		}

		if val, ok := data["sym"]; ok && f.Op == "deploy+mint" {
			f.Symbol = &val
		}

		if val, ok := data["icon"]; ok {
			if strings.HasPrefix(val, "_") {
				val = fmt.Sprintf("%x%s", txo.Outpoint.Txid(), val)
			}
			f.Icon, _ = lib.NewOutpointFromString(val)
		}

		if val, ok := data["contract"]; ok {
			f.Contract = &val
		}

		var hash [32]byte
		if f.Op == "deploy" {
			hash = sha256.Sum256([]byte(*f.Ticker))
		} else {
			hash = sha256.Sum256([]byte(f.Id.String()))
		}

		path := fmt.Sprintf("21/%d/%d", binary.BigEndian.Uint32(hash[:8])>>1, binary.BigEndian.Uint32(hash[24:])>>1)
		ek, err := hdKey.DeriveChildFromPath(path)
		if err != nil {
			log.Panic(err)
		}
		pubKey, err := ek.ECPubKey()
		if err != nil {
			log.Panic(err)
		}
		f.FundPath = path
		f.FundPKHash = crypto.Hash160(pubKey.SerialiseCompressed())
		return f, nil
	case "mint":
		if tick == nil {
			return nil, nil
		}
		fallthrough
	case "transfer":
		t := FungibleTxo{
			Id:     id,
			Op:     op,
			Ticker: tick,
		}
		if val, ok := data["amt"]; ok {
			t.Amt, err = strconv.ParseUint(val, 10, 64)
			if err != nil || t.Amt == 0 {
				return nil, nil
			}
		}
		return t, nil
	}
	return nil, nil
}

type TokenFunds struct {
	Ticker     *string       `json:"tick,omitempty"`
	Id         *lib.Outpoint `json:"id,omitempty"`
	PKHash     []byte        `json:"fundPKHash"`
	Total      int64         `json:"fundTotal"`
	Used       int64         `json:"fundUsed"`
	PendingOps uint32        `json:"pendingOps"`
	Pending    uint64        `json:"pending"`
	Included   bool          `json:"included"`
}

func (t *TokenFunds) TickID() string {
	if t.Id != nil {
		return t.Id.String()
	} else if t.Ticker != nil {
		return *t.Ticker
	}
	panic("No tick")
	// return ""
}

func (t *TokenFunds) Save() {
	if t.Total == 0 {
		return
	}
	if err := lib.Rdb.ZAdd(ctx, "f:fund:total", redis.Z{Score: float64(t.Total), Member: t.TickID()}).Err(); err != nil {
		panic(err)
	}

	fundsJson, err := json.Marshal(t)
	if err != nil {
		panic(err)
	}
	lib.Rdb.Publish(ctx, "tokenFunds", fundsJson)
	key := fmt.Sprintf("f:%s:func", t.TickID())
	if err := lib.Rdb.JSONSet(ctx, key, "$", fundsJson).Err(); err != nil {
		panic(err)
	}
	log.Println("Updated", string(fundsJson))
}

func (t *TokenFunds) Balance() int64 {
	return t.Total - t.Used
}

func (t *TokenFunds) UpdateFunding() {
	// log.Println("Updating funding for", t.Id.String())
	var total sql.NullInt64
	row := lib.Db.QueryRow(ctx, `
		SELECT SUM(satoshis)
		FROM bsv20_v2 b
		JOIN txos t ON t.pkhash=b.fund_pkhash
		WHERE b.fund_pkhash=$1`,
		t.PKHash,
	)

	err := row.Scan(&total)
	if err != nil && err != pgx.ErrNoRows {
		log.Panicln(err)
	}
	t.Total = total.Int64

	t.Used = 0
	t.PendingOps = 0

	if count, err := GetPendingOps(t.TickID()); err != nil {
		log.Panicln(err)
	} else {
		t.PendingOps += uint32(count)
	}
	if t.Used, err = GetFundUsed(t.TickID()); err != nil {
		log.Panicln(err)
	}
	t.Save()
}

func GetPendingOps(tickId string) (uint32, error) {
	if count, err := lib.Rdb.ZCount(ctx, "status:"+tickId, "0", "0").Result(); err != nil {
		return 0, err
	} else {
		return uint32(count), nil
	}
}

func GetFundUsed(tickId string) (int64, error) {
	if validCount, err := lib.Rdb.ZCount(ctx, "status:"+tickId, "1", "1").Result(); err != nil {
		return 0, err
	} else if invalidCount, err := lib.Rdb.ZCount(ctx, "status:"+tickId, "-1", "-1").Result(); err != nil {
		return 0, err
	} else {
		return (validCount + invalidCount) * FUNGIBLE_OP_COST, nil
	}
}

func GetFundTotal(tickId string) (int64, error) {
	if total, err := lib.Rdb.ZScore(ctx, "f:fund:total", tickId).Result(); err != nil {
		return 0, err
	} else {
		return int64(total), nil
	}
}

func InitializeFunding(concurrency int) map[string]*TokenFunds {
	idFunds := map[string]*TokenFunds{}
	limiter := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var m sync.Mutex
	iter := lib.Rdb.Scan(ctx, 0, "f:token:*", 0).Iterator()
	tickIds := make([]string, 0, 10000)
	for iter.Next(ctx) {
		key := iter.Val()
		tickIds = append(tickIds, strings.TrimPrefix(key, "f:token:"))
	}
	fmt.Println("Processing Fungible Funding")
	for i := 0; i < len(tickIds); i += 100 {
		keys := tickIds[i:min(i+100, len(tickIds))]
		jsons, err := lib.Rdb.JSONMGet(ctx, "$.fundPKHash", keys...).Result()
		if err != nil {
			panic(err)
		}
		for _, j := range jsons {
			if j == nil {
				continue
			}
			funds := TokenFunds{}
			if err := json.Unmarshal([]byte(j.(string)), &funds); err != nil {
				panic(err)
			}
			limiter <- struct{}{}
			wg.Add(1)
			go func(funds *TokenFunds) {
				defer func() {
					wg.Done()
					<-limiter
				}()
				add, err := bscript.NewAddressFromPublicKeyHash(funds.PKHash, true)
				if err != nil {
					log.Panicln(err)
				}
				ord.RefreshAddress(ctx, add.AddressString)

				funds.UpdateFunding()
				m.Lock()
				idFunds[funds.Id.String()] = funds
				m.Unlock()
			}(&funds)
		}
	}

	wg.Wait()
	return idFunds
}

// func ValidateBsv20Txos(height uint32) {
// 	rows, err := lib.Db.Query(ctx, `
// 		SELECT txid, vout, height, idx, tick, id, amt
// 		FROM bsv20_txos
// 		WHERE status=0 AND height <= $1 AND height IS NOT NULL
// 		ORDER BY height ASC, idx ASC, vout ASC`,
// 		height,
// 	)
// 	if err != nil {
// 		log.Panic(err)
// 	}
// 	defer rows.Close()

// 	ticks := map[string]*Bsv20{}
// 	var prevTxid []byte
// 	for rows.Next() {
// 		bsv20 := &Bsv20{}
// 		err = rows.Scan(&bsv20.Txid, &bsv20.Vout, &bsv20.Height, &bsv20.Idx, &bsv20.Ticker, &bsv20.Id, &bsv20.Amt)
// 		if err != nil {
// 			log.Panic(err)
// 		}

// 		switch bsv20.Op {
// 		case "mint":
// 			var reason string
// 			ticker, ok := ticks[bsv20.Ticker]
// 			if !ok {
// 				ticker = LoadTicker(bsv20.Ticker)
// 				ticks[bsv20.Ticker] = ticker
// 			}
// 			if ticker == nil {
// 				reason = fmt.Sprintf("invalid ticker %s as of %d %d", bsv20.Ticker, &bsv20.Height, &bsv20.Idx)
// 			} else if ticker.Supply >= ticker.Max {
// 				reason = fmt.Sprintf("supply %d >= max %d", ticker.Supply, ticker.Max)
// 			} else if ticker.Limit > 0 && *bsv20.Amt > ticker.Limit {
// 				reason = fmt.Sprintf("amt %d > limit %d", *bsv20.Amt, ticker.Limit)
// 			}

// 			if reason != "" {
// 				_, err = lib.Db.Exec(ctx, `
// 				UPDATE bsv20_txos
// 				SET status=-1, reason=$3
// 				WHERE txid=$1 AND vout=$2`,
// 					bsv20.Txid,
// 					bsv20.Vout,
// 					reason,
// 				)
// 				if err != nil {
// 					log.Panic(err)
// 				}
// 				continue
// 			}

// 			t, err := lib.Db.Begin(ctx)
// 			if err != nil {
// 				log.Panic(err)
// 			}
// 			defer t.Rollback(ctx)

// 			if ticker.Max-ticker.Supply < *bsv20.Amt {
// 				reason = fmt.Sprintf("supply %d + amt %d > max %d", ticker.Supply, *bsv20.Amt, ticker.Max)
// 				*bsv20.Amt = ticker.Max - ticker.Supply

// 				_, err := t.Exec(ctx, `
// 				UPDATE bsv20_txos
// 				SET status=1, amt=$3, reason=$4
// 				WHERE txid=$1 AND vout=$2`,
// 					bsv20.Txid,
// 					bsv20.Vout,
// 					*bsv20.Amt,
// 					reason,
// 				)
// 				if err != nil {
// 					log.Panic(err)
// 				}
// 			} else {
// 				_, err := t.Exec(ctx, `
// 				UPDATE bsv20_txos
// 				SET status=1
// 				WHERE txid=$1 AND vout=$2`,
// 					bsv20.Txid,
// 					bsv20.Vout,
// 				)
// 				if err != nil {
// 					log.Panic(err)
// 				}
// 			}

// 			ticker.Supply += *bsv20.Amt
// 			_, err = t.Exec(ctx, `
// 				UPDATE bsv20
// 				SET supply=$3
// 				WHERE txid=$1 AND vout=$2`,
// 				ticker.Txid,
// 				ticker.Vout,
// 				ticker.Supply,
// 			)
// 			if err != nil {
// 				log.Panic(err)
// 			}

// 			err = t.Commit(ctx)
// 			if err != nil {
// 				log.Panic(err)
// 			}
// 			fmt.Println("Validated Mint:", bsv20.Ticker, ticker.Supply, ticker.Max)
// 		case "transfer":
// 			if bytes.Equal(prevTxid, bsv20.Txid) {
// 				continue
// 			}
// 			prevTxid = bsv20.Txid
// 			ValidateV1Transfer(bsv20.Txid, bsv20.Ticker, true)
// 			fmt.Printf("Validated Transfer: %s %x\n", bsv20.Ticker, bsv20.Txid)
// 		}

// 	}
// }

// func ValidateV1Transfer(txid []byte, tick string, mined bool) int {
// 	// log.Printf("Validating %x %s\n", txid, tick)

// 	inRows, err := lib.Db.Query(ctx, `
// 		SELECT txid, vout, status, amt
// 		FROM bsv20_txos
// 		WHERE spend=$1 AND tick=$2`,
// 		txid,
// 		tick,
// 	)
// 	if err != nil {
// 		log.Panicf("%x - %v\n", txid, err)
// 	}
// 	defer inRows.Close()

// 	var reason string
// 	var tokensIn uint64
// 	var tokenOuts []uint32
// 	for inRows.Next() {
// 		var inTxid []byte
// 		var vout uint32
// 		var amt uint64
// 		var inStatus int
// 		err = inRows.Scan(&inTxid, &vout, &inStatus, &amt)
// 		if err != nil {
// 			log.Panicf("%x - %v\n", txid, err)
// 		}

// 		switch inStatus {
// 		case -1:
// 			reason = "invalid input"
// 		case 0:
// 			fmt.Printf("inputs pending %s %x\n", tick, txid)
// 			return 0
// 		case 1:
// 			tokensIn += amt
// 		}
// 	}
// 	inRows.Close()

// 	sql := `SELECT vout, status, amt
// 		FROM bsv20_txos
// 		WHERE txid=$1 AND tick=$2 AND op='transfer'`
// 	outRows, err := lib.Db.Query(ctx,
// 		sql,
// 		txid,
// 		tick,
// 	)
// 	if err != nil {
// 		log.Panicf("%x - %v\n", txid, err)
// 	}
// 	defer outRows.Close()

// 	for outRows.Next() {
// 		var vout uint32
// 		var amt uint64
// 		var status int
// 		err = outRows.Scan(&vout, &status, &amt)
// 		if err != nil {
// 			log.Panicf("%x - %v\n", txid, err)
// 		}
// 		tokenOuts = append(tokenOuts, vout)
// 		if amt > tokensIn {
// 			if reason == "" {
// 				reason = fmt.Sprintf("insufficient balance %d < %d", tokensIn, amt)
// 			}
// 			if !mined {
// 				fmt.Printf("%s %s - %x\n", tick, reason, txid)
// 				return 0
// 			}
// 		} else {
// 			tokensIn -= amt
// 		}
// 	}
// 	outRows.Close()

// 	if reason != "" {
// 		log.Printf("Transfer Invalid: %x %s %s\n", txid, tick, reason)
// 		sql := `UPDATE bsv20_txos
// 			SET status=-1, reason=$3
// 			WHERE txid=$1 AND vout=ANY($2)`
// 		_, err := lib.Db.Exec(ctx, sql,
// 			txid,
// 			tokenOuts,
// 			reason,
// 		)
// 		if err != nil {
// 			log.Panicf("%x %v\n", txid, err)
// 		}
// 	} else {
// 		log.Printf("Transfer Valid: %x %s\n", txid, tick)
// 		rows, err := lib.Db.Query(ctx, `
// 			UPDATE bsv20_txos
// 			SET status=1
// 			WHERE txid=$1 AND vout=ANY ($2)
// 			RETURNING vout, height, idx, amt, pkhash, listing, price, price_per_token, script`,
// 			txid,
// 			tokenOuts,
// 		)
// 		if err != nil {
// 			log.Panicf("%x %v\n", txid, err)
// 		}
// 		defer rows.Close()
// 		for rows.Next() {
// 			bsv20 := Bsv20{
// 				Txid:   txid,
// 				Ticker: tick,
// 			}
// 			err = rows.Scan(&bsv20.Vout, &bsv20.Height, &bsv20.Idx, &bsv20.Amt, &bsv20.PKHash, &bsv20.Listing, &bsv20.Price, &bsv20.PricePerToken, &bsv20.Script)
// 			if err != nil {
// 				log.Panicf("%x %v\n", txid, err)
// 			}

// 			log.Printf("Validating %s %x %d\n", tick, txid, bsv20.Vout)
// 			if bsv20.Listing {
// 				bsv20.Outpoint = lib.NewOutpoint(txid, bsv20.Vout)
// 				add, err := bscript.NewAddressFromPublicKeyHash(bsv20.PKHash, true)
// 				if err == nil {
// 					bsv20.Owner = add.AddressString
// 				}
// 				out, err := json.Marshal(bsv20)
// 				if err != nil {
// 					log.Panic(err)
// 				}
// 				// log.Println("Publishing", string(out))
// 				Rdb.Publish(context.Background(), "bsv20listings", out)
// 			}
// 		}
// 	}
// 	return len(tokenOuts)
// }
