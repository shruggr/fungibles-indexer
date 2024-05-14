package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/GorillaPool/go-junglebus"
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
var cache *redis.Client
var INDEXER string
var TOPIC string
var FROM_BLOCK uint
var VERBOSE int
var CONCURRENCY int = 1
var ctx = context.Background()

const REFRESH = 15 * time.Second

var tip *models.BlockHeader

func init() {
	wd, _ := os.Getwd()
	log.Println("CWD:", wd)
	godotenv.Load(fmt.Sprintf(`%s/../../.env`, wd))

	flag.StringVar(&INDEXER, "id", "inscriptions", "Indexer name")
	flag.StringVar(&TOPIC, "t", "", "Junglebus SubscriptionID")
	flag.UintVar(&FROM_BLOCK, "s", uint(lib.TRIGGER), "Start from block")
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
		Addr:     os.Getenv("REDISDB"),
		Password: "", // no password set
		DB:       0,  // use default DB
	})

	cache = redis.NewClient(&redis.Options{
		Addr:     os.Getenv("REDISCAHCE"),
		Password: "", // no password set
		DB:       0,  // use default DB
	})

	lib.Initialize(db, rdb, cache)
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

	if INDEXER != "" {
		progress, err := rdb.HGet(ctx, "PROGRESS", INDEXER).Uint64()
		if err != nil && err != redis.Nil {
			log.Panic(err)
		}
		if progress > 6 {
			progress -= 6
		}
		if progress > uint64(FROM_BLOCK) {
			FROM_BLOCK = uint(progress)
		}
	}

	var txCount int
	var height uint32
	var idx uint64
	ticker := time.NewTicker(10 * time.Second)
	go func() {
		for range ticker.C {
			if txCount > 0 {
				log.Printf("Blk %d I %d - %d txs %d/s\n", height, idx, txCount, txCount/10)
			}
			txCount = 0
		}
	}()

	var sub *junglebus.Subscription
	var eventHandler junglebus.EventHandler
	eventHandler = junglebus.EventHandler{
		OnStatus: func(status *models.ControlResponse) {
			if VERBOSE > 0 {
				log.Printf("[STATUS]: %d %v\n", status.StatusCode, status.Message)
			}
			if status.StatusCode == 200 {
				height = status.Block
				if INDEXER != "" {
					if err := rdb.HSet(ctx, "PROGRESS", INDEXER, height).Err(); err != nil {
						log.Panic(err)
					}
				}
				FROM_BLOCK = uint(status.Block) + 1
				if FROM_BLOCK > uint(tip.Height-5) {
					sub.Unsubscribe()
					ticker := time.NewTicker(REFRESH)
					for range ticker.C {
						if FROM_BLOCK <= uint(tip.Height-5) {
							sub = subscribe(eventHandler)
							break
						}
					}
				}
			}
			if status.StatusCode == 999 {
				log.Println(status.Message)
				log.Println("Unsubscribing...")
				sub.Unsubscribe()
				os.Exit(0)
				return
			}
		},
		OnError: func(err error) {
			log.Panicf("[ERROR]: %v\n", err)
		},
		OnTransaction: func(txn *models.TransactionResponse) {
			if VERBOSE > 0 {
				log.Printf("[TX]: %d - %d: %d %s\n", txn.BlockHeight, txn.BlockIndex, len(txn.Transaction), txn.Id)
			}
			txCount++
			height = txn.BlockHeight
			idx = txn.BlockIndex
			txCtx, err := lib.ParseTxn(txn.Transaction, txn.BlockHash, txn.BlockHeight, txn.BlockIndex)
			if err != nil {
				log.Panicln(txn.Id, err)
			}
			ordinals.IndexFungibles(txCtx)
			// if INDEXER != "" {
			// rdb.SAdd(ctx, "TXLOG", txn.Id)
			// }
		},
		// OnMempool: func(txn *models.TransactionResponse) {
		// 	if VERBOSE > 0 {
		// 		log.Printf("[MEMPOOL]: %d %s\n", len(txn.Transaction), txn.Id)
		// 	}
		// 	txCtx, err := lib.ParseTxn(txn.Transaction, txn.BlockHash, txn.BlockHeight, txn.BlockIndex)
		// 	if err != nil {
		// 		log.Panicln(txn.Id, err)
		// 	}
		// 	ordinals.IndexFungiblesMempool(txCtx)
		// },
	}

	log.Println("Subscribing to Junglebus from block", FROM_BLOCK)
	sub = subscribe(eventHandler)
	defer func() {
		sub.Unsubscribe()
	}()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		fmt.Printf("Caught signal")
		fmt.Println("Unsubscribing and exiting...")
		sub.Unsubscribe()
		os.Exit(0)
	}()

	<-make(chan struct{})
}

func subscribe(eventHandler junglebus.EventHandler) *junglebus.Subscription {
	sub, err := lib.JB.Subscribe(
		context.Background(),
		TOPIC,
		uint64(FROM_BLOCK),
		eventHandler,
	)
	if err != nil {
		panic(err)
	}
	return sub
}
