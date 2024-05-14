package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GorillaPool/go-junglebus/models"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
	"github.com/shruggr/fungibles-indexer/indexer"
	"github.com/shruggr/fungibles-indexer/lib"
	"github.com/shruggr/fungibles-indexer/ordinals"
)

// var settled = make(chan uint32, 1000)
var POSTGRES string
var db *pgxpool.Pool
var rdb *redis.Client
var INDEXER string
var TOPIC string
var FROM_BLOCK uint
var VERBOSE int
var CONCURRENCY int
var ctx = context.Background()
var pkhashFunds = map[string]*ordinals.TokenFunds{}
var tickIdFunds = map[string]*ordinals.TokenFunds{}
var m sync.Mutex
var sub *redis.PubSub

const REFRESH = 15 * time.Second

var tip *models.BlockHeader

func init() {
	wd, _ := os.Getwd()
	log.Println("CWD:", wd)
	godotenv.Load(fmt.Sprintf(`%s/../../.env`, wd))

	flag.StringVar(&INDEXER, "id", "inscriptions", "Indexer name")
	flag.StringVar(&TOPIC, "t", "", "Junglebus SubscriptionID")
	flag.UintVar(&FROM_BLOCK, "s", uint(lib.TRIGGER), "Start from block")
	flag.IntVar(&CONCURRENCY, "c", 64, "Concurrency Limit")
	flag.IntVar(&VERBOSE, "v", 0, "Verbose")
	flag.Parse()

	if POSTGRES == "" {
		POSTGRES = os.Getenv("POSTGRES_FULL")
	}
	var err error
	log.Println("POSTGRES:", POSTGRES)
	db, err = pgxpool.New(ctx, POSTGRES)
	if err != nil {
		log.Panic(err)
	}

	rdb = redis.NewClient(&redis.Options{
		Addr:     os.Getenv("REDIS"),
		Password: "", // no password set
		DB:       0,  // use default DB
	})

	err = indexer.Initialize(db, rdb)
	if err != nil {
		log.Panic(err)
	}

	err = ordinals.Initialize(indexer.Db, indexer.Rdb)
	if err != nil {
		log.Panic(err)
	}
}

func main() {
	var err error
	if tip, err = lib.JB.GetChaintip(ctx); err != nil {
		log.Panic(err)
	}
	go func() {
		ticker := time.NewTicker(REFRESH)
		for range ticker.C {
			if newTip, err := lib.JB.GetChaintip(ctx); err != nil {
				log.Println("GetChaintip", err)
			} else {
				tip = newTip
			}
		}
	}()
	subRdb := redis.NewClient(&redis.Options{
		Addr:     os.Getenv("REDIS"),
		Password: "", // no password set
		DB:       0,  // use default DB
	})
	sub = subRdb.Subscribe(ctx, "v2xfer")
	ch1 := sub.Channel()

	funds := rdb.HGetAll(ctx, "FUND").Val()
	for tickId, j := range funds {
		funds := ordinals.TokenFunds{}
		err := json.Unmarshal([]byte(j), &funds)
		if err != nil {
			log.Panic(err)
		}
		pkhash := hex.EncodeToString(funds.PKHash)
		pkhashFunds[pkhash] = &funds
		m.Lock()
		tickIdFunds[tickId] = &funds
		m.Unlock()
		sub.Subscribe(ctx, pkhash)
	}

	go func() {
		for {
			m.Lock()
			tickIdFunds = ordinals.InitializeFunding(CONCURRENCY)
			for _, funds := range tickIdFunds {
				pkhash := hex.EncodeToString(funds.PKHash)
				if _, ok := pkhashFunds[pkhash]; !ok {
					pkhashFunds[pkhash] = funds
					sub.Subscribe(ctx, pkhash)
				}
			}
			m.Unlock()
			time.Sleep(time.Hour)
		}
	}()

	go func() {
		for msg := range ch1 {
			switch msg.Channel {
			case "tokenFunds":
				funds := &ordinals.TokenFunds{}
				err := json.Unmarshal([]byte(msg.Payload), &funds)
				if err != nil {
					break
				}
				m.Lock()
				tickIdFunds[funds.Id.String()] = funds
				m.Unlock()
				pkhash := hex.EncodeToString(funds.PKHash)
				pkhashFunds[pkhash] = funds
			case "tokenXfer":
				// parts := strings.Split(msg.Payload, ":")
				// txid, err := hex.DecodeString(parts[0])
				// if err != nil {
				// 	log.Println("Decode err", err)
				// 	break
				// }
				// tokenId, err := lib.NewOutpointFromString(parts[1])
				// if err != nil {
				// 	log.Println("NewOutpointFromString err", err)
				// 	break
				// }
				// if funds, ok := tickIdFunds[tokenId.String()]; ok {
				// 	outputs := ordinals.ValidateV2Transfer(txid, tokenId, false)
				// 	funds.Used += int64(outputs) * ordinals.BSV20V2_OP_COST
				// }
			default:
				if funds, ok := pkhashFunds[msg.Channel]; ok {
					log.Println("Updating funding", funds.Id.String())
					funds.UpdateFunding()
				}
			}
		}
	}()

	for {
		if !processFungibles() {
			log.Println("No work to do")
			time.Sleep(time.Minute)
		}
	}

}

func processFungibles() (didWork bool) {
	var wg sync.WaitGroup
	limiter := make(chan struct{}, 8)
	m.Lock()
	fundsList := make([]*ordinals.TokenFunds, 0, len(tickIdFunds))
	for _, funds := range tickIdFunds {
		if funds.Balance() >= ordinals.BSV20V2_OP_COST {
			fundsList = append(fundsList, funds)
		}
	}
	m.Unlock()

	for _, funds := range fundsList {
		if funds.Balance() < ordinals.BSV20V2_OP_COST {
			continue
		}

		log.Println("Processing ", funds.Id.String(), funds.Balance())
		wg.Add(1)
		limiter <- struct{}{}
		go func(funds *ordinals.TokenFunds) {
			defer func() {
				<-limiter
				wg.Done()
			}()
			token, err := ordinals.LoadFungible(funds.TickID(), false)
			if err != nil {
				panic(err)
			}
			if token == nil {
				return
			}
			limit := funds.Balance() / ordinals.BSV20V2_OP_COST
			tickKey := fmt.Sprintf("FVALIDATE:%s:", funds.TickID())
			blockIter := rdb.Scan(ctx, 0, tickKey+"*", 0).Iterator()
			for blockIter.Next(ctx) {
				key := blockIter.Val()
				var height uint64
				if height, err = strconv.ParseUint(strings.TrimPrefix(key, tickKey), 10, 32); err != nil {
					panic(err)
				}
				iter := rdb.ZScan(ctx, key, 0, "", limit).Iterator()
				var prevTxid []byte
				for iter.Next(ctx) {
					outpoint, err := lib.NewOutpointFromString(iter.Val())
					if err != nil {
						panic(err)
					}
					ftxo, err := ordinals.LoadFungibleTxo(*outpoint, true)
					switch ftxo.Op {
					case "mint":
						if ftxo.Height > tip.Height-5 {
							break
						}
						var reason string
						if *token.Supply >= token.Max {
							reason = fmt.Sprintf("supply %d >= max %d", *token.Supply, token.Max)
						} else if *token.Limit > 0 && ftxo.Amt > *token.Limit {
							reason = fmt.Sprintf("amt %d > limit %d", ftxo.Amt, *token.Limit)
						}
						if reason != "" {
							ftxo.Reason = &reason
							ftxo.Save()
							rdb.ZRem(ctx, key, ftxo.Outpoint.String())
							break
						}
						if token.Max-*token.Supply < ftxo.Amt {
							reason = fmt.Sprintf("supply %d + amt %d > max %d", *token.Supply, ftxo.Amt, token.Max)
							ftxo.Amt = token.Max - *token.Supply
							ftxo.Reason = &reason
							ftxo.Status = int(ordinals.Valid)
						} else {
							ftxo.Status = int(ordinals.Valid)
						}
						ftxo.Save()
						*token.Supply += ftxo.Amt
						rdb.Pipelined(ctx, func(pipe redis.Pipeliner) error {
							pipe.JSONSet(ctx, "FUNGIBLE:"+funds.TickID(), "$.supply", *token.Supply)
							pipe.ZRem(ctx, key, ftxo.Outpoint.String())
							return nil
						})
						funds.Used += ordinals.FUNGIBLE_OP_COST
						fmt.Println("Validated Mint:", funds.TickID(), *token.Supply, token.Max)
						didWork = true
					case "transfer":
						if bytes.Equal(prevTxid, ftxo.Outpoint.Txid()) {
							// fmt.Printf("Skipping: %s %x\n", funds.Id.String(), bsv20.Txid)
							continue
						}
						prevTxid = ftxo.Outpoint.Txid()
						outputs := ordinals.ValidateV2Transfer(ftxo.Txid, funds.Id, ftxo.Height != nil && *ftxo.Height <= currentHeight)
						if outputs > 0 {
							didWork = true
						}
						funds.Used += int64(outputs) * ordinals.BSV20V2_OP_COST
						fmt.Printf("Validated Transfer: %s %x\n", funds.Id.String(), ftxo.Txid)
						if err = pipe.ZRem(ctx, validateKey, b.Outpoint.String()).Err(); err != nil {
							panic(err)
						}
					}
				}

			}
			// if didWork {
			// 	funds.UpdateFunding()
			// }

		}(funds)
	}
	wg.Wait()

	return
}
