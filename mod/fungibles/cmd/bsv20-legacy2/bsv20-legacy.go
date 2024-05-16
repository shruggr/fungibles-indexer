package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"

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

var TOPIC string
var VERBOSE int
var ctx = context.Background()

func init() {
	wd, _ := os.Getwd()
	log.Println("CWD:", wd)
	godotenv.Load(fmt.Sprintf(`%s/../../.env`, wd))
	flag.StringVar(&TOPIC, "t", "", "Junglebus SubscriptionID")
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
	var listenWg sync.WaitGroup
	listenWg.Add(1)
	var sub *junglebus.Subscription
	eventHandler := junglebus.EventHandler{
		OnStatus: func(status *models.ControlResponse) {
			if VERBOSE > 0 {
				log.Printf("[STATUS]: %d %v\n", status.StatusCode, status.Message)
			}
			if status.StatusCode == 200 {
				if status.Block+1 >= 793000 {
					sub.Unsubscribe()
					listenWg.Done()
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

			if score, err := strconv.ParseFloat(fmt.Sprintf("%06d.%08d", txn.BlockHeight, txn.BlockIndex), 64); err != nil {
				log.Panic(err)
			} else if err := rdb.ZAdd(ctx, "bsv20Legacy", redis.Z{
				Score:  score,
				Member: txn.Id,
			}).Err(); err != nil {
				log.Panic(err)
			}
		},
		OnError: func(err error) {
			log.Panicf("[ERROR]: %v\n", err)
		},
	}

	log.Println("Subscribing to Junglebus from block", 783968)
	sub, err := lib.JB.Subscribe(
		context.Background(),
		TOPIC,
		uint64(783968),
		eventHandler,
	)
	if err != nil {
		panic(err)
	}

	defer sub.Unsubscribe()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		fmt.Printf("Caught signal")
		fmt.Println("Unsubscribing and exiting...")
		sub.Unsubscribe()
		os.Exit(0)
	}()

	for _, txid := range Txids {
		if tx, err := lib.JB.GetTransaction(ctx, txid); err != nil {
			log.Println("GetTransaction", err)
		} else {
			if score, err := strconv.ParseFloat(fmt.Sprintf("%06d.%08d", tx.BlockHeight, tx.BlockIndex), 64); err != nil {
				log.Panic(err)
			} else if err := rdb.ZAdd(ctx, "bsv20Legacy", redis.Z{
				Score:  score,
				Member: tx.ID.String(),
			}).Err(); err != nil {
				log.Panic(err)
			}
		}
	}
	listenWg.Wait()

}
