package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/GorillaPool/go-junglebus"
	"github.com/GorillaPool/go-junglebus/models"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
	"github.com/shruggr/fungibles-indexer/lib"
)

// var settled = make(chan uint32, 1000)
var POSTGRES string
var db *pgxpool.Pool
var rdb *redis.Client
var cache *redis.Client

var INDEXER string
var TOPIC string
var VERBOSE int
var ctx = context.Background()

const REFRESH = 15 * time.Second

var tip *models.BlockHeader
var progress uint
var lastBlock uint32
var lastIdx uint64

func init() {
	wd, _ := os.Getwd()
	log.Println("CWD:", wd)
	godotenv.Load(fmt.Sprintf(`%s/../../.env`, wd))

	flag.StringVar(&INDEXER, "id", "inscriptions", "Indexer name")
	flag.StringVar(&TOPIC, "t", "", "Junglebus SubscriptionID")
	flag.UintVar(&progress, "s", uint(lib.TRIGGER), "Start from block")
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
	tip, err = lib.JB.GetChaintip(ctx)
	if err != nil {
		panic(err)
	}
	go func() {
		for {
			time.Sleep(REFRESH)
			if tip, err = lib.JB.GetChaintip(ctx); err != nil {
				log.Println("GetChaintip", err)
			}
		}
	}()

	if INDEXER != "" {
		if logs, err := rdb.XRevRangeN(ctx, "idx:log:"+INDEXER, "+", "-", 1).Result(); err != nil {
			log.Panic(err)
		} else if len(logs) > 0 {
			parts := strings.Split(logs[0].ID, "-")
			if height, err := strconv.ParseUint(parts[0], 10, 32); err == nil && height > uint64(progress) {
				progress = uint(height)
				lastBlock = uint32(progress)
			} else if idx, err := strconv.ParseUint(parts[1], 10, 64); err == nil {
				lastIdx = idx
			}
		}
	}

	var txCount int
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		for range ticker.C {
			if txCount > 0 {
				log.Printf("Blk %d I %d - %d txs %d/s\n", lastBlock, lastIdx, txCount, txCount/10)
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
				progress = uint(status.Block) + 1
				if progress > uint(tip.Height-5) {
					sub.Unsubscribe()
					ticker := time.NewTicker(REFRESH)
					for range ticker.C {
						if progress <= uint(tip.Height-5) {
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
		OnTransaction: func(txn *models.TransactionResponse) {
			if VERBOSE > 0 {
				log.Printf("[TX]: %d %s\n", len(txn.Transaction), txn.Id)
			}
			if txn.BlockHeight < lastBlock || (txn.BlockHeight == lastBlock && txn.BlockIndex <= lastIdx) {
				return
			}
			if err := rdb.XAdd(ctx, &redis.XAddArgs{
				Stream: "idx:log:" + INDEXER,
				Values: map[string]interface{}{
					"txn": txn.Id,
				},
				ID: fmt.Sprintf("%d-%d", txn.BlockHeight, txn.BlockIndex),
			}).Err(); err != nil {
				log.Panic(err)
			}
			lastBlock = txn.BlockHeight
			lastIdx = txn.BlockIndex
			txCount++
		},
		OnError: func(err error) {
			log.Panicf("[ERROR]: %v\n", err)
		},
	}

	log.Println("Subscribing to Junglebus from block", progress)
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
	if sub, err := lib.JB.SubscribeWithQueue(
		context.Background(),
		TOPIC,
		uint64(progress),
		0,
		eventHandler,
		&junglebus.SubscribeOptions{
			QueueSize: 100000,
			LiteMode:  true,
		},
	); err != nil {
		panic(err)
	} else {
		return sub
	}
}
