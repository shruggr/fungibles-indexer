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
	"sync"
	"time"

	"github.com/GorillaPool/go-junglebus/models"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
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
		if funds.Balance() >= ordinals.FUNGIBLE_OP_COST {
			fundsList = append(fundsList, funds)
		}
	}
	m.Unlock()

	for _, funds := range fundsList {
		if funds.Balance() < ordinals.FUNGIBLE_OP_COST {
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
			tickId := funds.TickID()
			token, err := ordinals.LoadFungible(tickId, false)
			if err != nil {
				panic(err)
			}
			if token == nil {
				return
			}
			limit := funds.Balance() / ordinals.FUNGIBLE_OP_COST
			var supply uint64
			if fSupply, err := rdb.ZScore(ctx, "FSUPPLY", tickId).Result(); err != nil {
				panic(err)
			} else {
				supply = uint64(fSupply)
			}
			tickKey := fmt.Sprintf("FVALIDATE:%s:", tickId)
			blockIter := rdb.Scan(ctx, 0, tickKey+"*", 0).Iterator()

			for blockIter.Next(ctx) {
				validateKey := blockIter.Val()
				iter := rdb.ZScan(ctx, validateKey, 0, "", limit).Iterator()
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
						if supply >= token.Max {
							reason = fmt.Sprintf("supply %d >= max %d", supply, token.Max)
						} else if *token.Limit > 0 && ftxo.Amt > *token.Limit {
							reason = fmt.Sprintf("amt %d > limit %d", ftxo.Amt, *token.Limit)
						}
						if reason != "" {
							if _, err = rdb.Pipelined(ctx, func(pipe redis.Pipeliner) error {
								ftxo.SetStatus(pipe, -1, reason)
								pipe.ZRem(ctx, validateKey, ftxo.Outpoint.String())
								return nil
							}); err != nil {
								panic(err)
							}
							break
						}
						if token.Max-supply < ftxo.Amt {
							reason = fmt.Sprintf("supply %d + amt %d > max %d", supply, ftxo.Amt, token.Max)
							ftxo.Amt = token.Max - supply
							ftxo.Reason = &reason
							ftxo.Status = int(ordinals.Valid)
						} else {
							ftxo.Status = int(ordinals.Valid)
						}
						ftxo.Save()
						supply -= ftxo.Amt
						if _, err = rdb.Pipelined(ctx, func(pipe redis.Pipeliner) error {
							token.DecrementSupply(pipe, ftxo.Amt)
							pipe.ZRem(ctx, validateKey, ftxo.Outpoint.String())
							return nil
						}); err != nil {
							panic(err)
						}
						funds.Used += ordinals.FUNGIBLE_OP_COST
						fmt.Println("Validated Mint:", tickId, supply, token.Max)
						didWork = true
					case "transfer":
						if bytes.Equal(prevTxid, ftxo.Outpoint.Txid()) {
							break
						}
						prevTxid = ftxo.Outpoint.Txid()
						outputs, aborted := ordinals.ValidateV2Transfer(ftxo.Outpoint.Txid(), tickId, ftxo.Height == 0)
						if aborted {
							break
						}

						if outputs > 0 {
							didWork = true
						}
						// if
						funds.Used += int64(outputs) * ordinals.FUNGIBLE_OP_COST
						fmt.Printf("Validated Transfer: %s %x\n", tickId, ftxo.Outpoint.Txid())
						if err = rdb.ZRem(ctx, validateKey, ftxo.Outpoint.String()).Err(); err != nil {
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
