package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/GorillaPool/go-junglebus"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/gofiber/swagger"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
	_ "github.com/shruggr/fungibles-indexer/cmd/server/docs"
	"github.com/shruggr/fungibles-indexer/lib"
	"github.com/shruggr/fungibles-indexer/ordinals"
)

var POSTGRES string
var CONCURRENCY int
var PORT int
var db *pgxpool.Pool
var rdb *redis.Client
var cache *redis.Client
var jb *junglebus.Client

const INCLUDE_THREASHOLD = 10000000
const HOLDER_CACHE_TIME = 24 * time.Hour

func init() {
	wd, _ := os.Getwd()
	log.Println("CWD:", wd)
	godotenv.Load(fmt.Sprintf(`%s/../../.env`, wd))

	if POSTGRES == "" {
		POSTGRES = os.Getenv("POSTGRES_FULL")
	}

	log.Println("POSTGRES:", POSTGRES)
	var err error
	config, err := pgxpool.ParseConfig(POSTGRES)
	if err != nil {
		log.Panic(err)
	}
	config.MaxConnIdleTime = 15 * time.Second

	db, err = pgxpool.NewWithConfig(context.Background(), config)

	// db, err = pgxpool.New(context.Background(), POSTGRES)
	if err != nil {
		log.Panic(err)
	}

	rdb = redis.NewClient(&redis.Options{
		Addr:     os.Getenv("REDISDB"),
		Password: "", // no password set
		DB:       0,  // use default DB
	})

	cache = redis.NewClient(&redis.Options{
		Addr:     os.Getenv("REDISCACHE"),
		Password: "", // no password set
		DB:       0,  // use default DB
	})

	JUNGLEBUS := os.Getenv("JUNGLEBUS")
	if JUNGLEBUS == "" {
		JUNGLEBUS = "https://junglebus.gorillapool.io"
	}

	jb, err = junglebus.New(
		junglebus.WithHTTP(JUNGLEBUS),
	)
	if err != nil {
		log.Panicln(err.Error())
	}

	lib.Initialize(db, rdb, cache)
}

// @title BSV20/21 Fungibles API
// @version 1.0
// @description This is a sample server server.
// @schemes http
func main() {
	// flag.IntVar(&CONCURRENCY, "c", 64, "Concurrency Limit")
	flag.IntVar(&PORT, "p", 8082, "Port to listen on")
	flag.Parse()

	app := fiber.New()
	app.Use(recover.New())
	app.Use(logger.New())

	app.Get("/", HealthCheck)
	app.Get("/swagger/*", swagger.HandlerDefault) // default

	app.Get("/yo", func(c *fiber.Ctx) error {
		return c.SendString("Yo!")
	})

	app.Get("/v1/tokens", func(c *fiber.Ctx) error {
		limit := c.QueryInt("limit", 100)
		offset := c.QueryInt("offset", 0)

		var err error
		var tickIds []string
		min := "-inf"
		if c.QueryBool("included", true) {
			min = fmt.Sprintf("%d", INCLUDE_THREASHOLD)
		}
		if tickIds, err = rdb.ZRevRangeByScore(c.Context(), "f:fund:total", &redis.ZRangeBy{
			Min:    min,
			Max:    "+inf",
			Count:  int64(limit),
			Offset: int64(offset),
		}).Result(); err != nil {
			return &fiber.Error{
				Code:    fiber.StatusInternalServerError,
				Message: err.Error(),
			}
		}
		if len(tickIds) == 0 {
			return c.JSON([]string{})
		}

		if txos, err := ordinals.LoadFungibles(tickIds); err != nil {
			return &fiber.Error{
				Code:    fiber.StatusInternalServerError,
				Message: err.Error(),
			}
		} else {
			return c.JSON(txos)
		}
	})

	app.Get("/v1/tokens/:tickId", func(c *fiber.Ctx) error {
		tickId := ordinals.FormatTickID(c.Params("tickId"))
		if token, err := ordinals.LoadFungible(tickId); err != nil {
			return &fiber.Error{
				Code:    fiber.StatusInternalServerError,
				Message: err.Error(),
			}
		} else if token == nil {
			return &fiber.Error{
				Code:    fiber.StatusNotFound,
				Message: "Not Found",
			}
		} else {
			resp := &TokenResponse{
				Fungible: *token,
			}
			if pendingOps, err := ordinals.GetPendingOps(tickId); err != nil {
				return &fiber.Error{
					Code:    fiber.StatusInternalServerError,
					Message: err.Error(),
				}
			} else if fundUsed, err := ordinals.GetFundUsed(tickId); err != nil {
				return &fiber.Error{
					Code:    fiber.StatusInternalServerError,
					Message: err.Error(),
				}
			} else if fundTotal, err := ordinals.GetFundTotal(tickId); err != nil {
				return &fiber.Error{
					Code:    fiber.StatusInternalServerError,
					Message: err.Error(),
				}
			} else {
				resp.PendingOps = pendingOps
				resp.Used = fundUsed
				resp.Total = fundTotal
				resp.Included = fundTotal >= INCLUDE_THREASHOLD
			}
			return c.JSON(resp)
		}
	})

	app.Get("/v1/tokens/outpoint/:outpoint", func(c *fiber.Ctx) error {
		if txo, err := ordinals.LoadFungibleTxo(c.Params("outpoint")); err != nil {
			return &fiber.Error{
				Code:    fiber.StatusInternalServerError,
				Message: err.Error(),
			}
		} else if txo == nil {
			return &fiber.Error{
				Code:    fiber.StatusNotFound,
				Message: "Not Found",
			}
		} else {
			return c.JSON(txo)
		}
	})

	app.Get("/v1/tokens/spends/:txid", func(c *fiber.Ctx) error {
		txid := c.Params("txid")
		if len(txid) != 64 {
			return &fiber.Error{
				Code:    fiber.StatusBadRequest,
				Message: "Invalid Txid",
			}
		}
		ctx := c.Context()
		outpoints := make([]string, 0, 10)
		keyIter := rdb.Scan(ctx, 0, fmt.Sprintf("f.input:%s:*", txid), 0).Iterator()

		for keyIter.Next(ctx) {
			key := keyIter.Val()
			outIter := rdb.ZScan(ctx, key, 0, "", 0).Iterator()
			for outIter.Next(ctx) {
				outpoints = append(outpoints, outIter.Val())
			}
		}
		if txos, err := ordinals.LoadFungibleTxos(outpoints); err != nil {
			return &fiber.Error{
				Code:    fiber.StatusInternalServerError,
				Message: err.Error(),
			}
		} else {
			return c.JSON(txos)
		}
	})

	app.Get("/v1/tokens/address/:address/:balance", func(c *fiber.Ctx) error {
		address := c.Params("address")
		balance := c.Params("balance")
		if len(address) == 0 || len(balance) == 0 {
			return &fiber.Error{
				Code:    fiber.StatusBadRequest,
				Message: "Invalid Parameters",
			}
		}
		ctx := c.Context()
		addKey := fmt.Sprintf("FADDTXO:%s:", address)
		keyIter := rdb.Scan(ctx, 0, addKey+"*", 0).Iterator()
		balances := make([]*TokenBalanceResponse, 0, 10)
		tickIds := make([]string, 0, 10)
		for keyIter.Next(ctx) {
			key := keyIter.Val()
			tickId := strings.TrimPrefix(key, addKey)
			tickIds = append(tickIds, tickId)
			balance := &TokenBalanceResponse{}

			if outpoints, err := rdb.ZRangeByScore(ctx, fmt.Sprintf("FADDTXO:%s:%s", address, tickId), &redis.ZRangeBy{
				Min: "0",
				Max: "1",
			}).Result(); err != nil {
				return &fiber.Error{
					Code:    fiber.StatusInternalServerError,
					Message: err.Error(),
				}
			} else if len(outpoints) == 0 {
				continue
			} else {
				for _, outpoint := range outpoints {
					// m := map[string]string{}
					if vals, err := rdb.HMGet(ctx, "TXO:"+outpoint, "status", "amt", "listing").Result(); err != nil {
						return &fiber.Error{
							Code:    fiber.StatusInternalServerError,
							Message: err.Error(),
						}
					} else if amt, err := strconv.ParseUint(vals[1].(string), 10, 64); err != nil {
						return &fiber.Error{
							Code:    fiber.StatusInternalServerError,
							Message: err.Error(),
						}
					} else {
						_, listing := vals[2].(string)
						switch vals[0].(string) {
						case "0":
							balance.All.Pending = amt
							if listing {
								balance.Listed.Pending = amt
							}
						case "1":
							balance.All.Confirmed = amt
							if listing {
								balance.Listed.Confirmed = amt
							}
						}
					}
				}
			}
			balances = append(balances, balance)
		}
		if len(tickIds) == 0 {
			return c.JSON([]string{})
		} else if tokens, err := ordinals.LoadFungibles(tickIds); err != nil {
			return &fiber.Error{
				Code:    fiber.StatusInternalServerError,
				Message: err.Error(),
			}
		} else {
			response := make([]*TokenBalanceResponse, 0, len(balances))
			for i, balance := range balances {
				token := tokens[i]
				if token == nil {
					continue
				}
				balance.Tick = token.Ticker
				balance.Id = token.Id
				balance.Symbol = token.Symbol
				balance.Decimals = token.Decimals
				balance.Icon = token.Icon
				response = append(response, balance)
			}
			return c.JSON(response)
		}
	})

	app.Get("/v1/tokens/:tickId/address/:address/unspent", func(c *fiber.Ctx) error {
		tickId := c.Params("tickId")
		address := c.Params("address")
		if len(tickId) == 0 || len(address) == 0 {
			return &fiber.Error{
				Code:    fiber.StatusBadRequest,
				Message: "Invalid Parameters",
			}
		}
		if outpoints, err := rdb.ZRangeByScore(c.Context(), fmt.Sprintf("FADDTXO:%s:%s", address, tickId), &redis.ZRangeBy{
			Min:    "0",
			Max:    "1",
			Offset: int64(c.QueryInt("offset", 0)),
			Count:  int64(c.QueryInt("limit", 100)),
		}).Result(); err != nil {
			return &fiber.Error{
				Code:    fiber.StatusInternalServerError,
				Message: err.Error(),
			}
		} else if len(outpoints) == 0 {
			return c.JSON([]string{})
		} else if txos, err := ordinals.LoadFungibleTxos(outpoints); err != nil {
			return &fiber.Error{
				Code:    fiber.StatusInternalServerError,
				Message: err.Error(),
			}
		} else {
			return c.JSON(txos)
		}
	})
	app.Get("/v1/tokens/:tickId/address/:address/history", func(c *fiber.Ctx) error {
		tickId := c.Params("tickId")
		address := c.Params("address")
		if len(tickId) == 0 || len(address) == 0 {
			return &fiber.Error{
				Code:    fiber.StatusBadRequest,
				Message: "Invalid Parameters",
			}
		}
		if outpoints, err := rdb.ZRangeByScore(c.Context(), fmt.Sprintf("FADDTXO:%s:%s", address, tickId), &redis.ZRangeBy{
			Min:    "1",
			Max:    "inf",
			Offset: int64(c.QueryInt("offset", 0)),
			Count:  int64(c.QueryInt("limit", 100)),
		}).Result(); err != nil {
			return &fiber.Error{
				Code:    fiber.StatusInternalServerError,
				Message: err.Error(),
			}
		} else if len(outpoints) == 0 {
			return c.JSON([]string{})
		} else if txos, err := ordinals.LoadFungibleTxos(outpoints); err != nil {
			return &fiber.Error{
				Code:    fiber.StatusInternalServerError,
				Message: err.Error(),
			}
		} else {
			return c.JSON(txos)
		}
	})

	app.Get("/v1/tokens/:tickId/market", func(c *fiber.Ctx) error {
		tickId := ordinals.FormatTickID(c.Params("tickId"))
		limit := c.QueryInt("limit", 100)
		offset := c.QueryInt("offset", 0)
		if token, err := ordinals.LoadFungible(tickId); err != nil {
			return &fiber.Error{
				Code:    fiber.StatusInternalServerError,
				Message: err.Error(),
			}
		} else if token == nil {
			return &fiber.Error{
				Code:    fiber.StatusNotFound,
				Message: "Not Found",
			}
		} else if outpoints, err := rdb.ZRangeByScore(c.Context(), "FLIST:"+tickId, &redis.ZRangeBy{
			Min:    "1",
			Max:    "+inf",
			Offset: int64(offset),
			Count:  int64(limit),
		}).Result(); err != nil {
			return &fiber.Error{
				Code:    fiber.StatusInternalServerError,
				Message: err.Error(),
			}
		} else if len(outpoints) == 0 {
			return c.JSON([]string{})
		} else if txos, err := ordinals.LoadFungibleTxos(outpoints); err != nil {
			return &fiber.Error{
				Code:    fiber.StatusInternalServerError,
				Message: err.Error(),
			}
		} else {
			response := &TokenTxosResponse{
				Token: token,
				Txos:  make([]*ordinals.FungibleTxo, 0, len(txos)),
			}
			for _, txo := range txos {
				if txo != nil {
					response.Txos = append(response.Txos, txo)
				}
			}
			return c.JSON(response)
		}
	})

	app.Get("/v1/tokens/:tickId/sales", func(c *fiber.Ctx) error {
		tickId := ordinals.FormatTickID(c.Params("tickId"))
		limit := c.QueryInt("limit", 100)
		offset := c.QueryInt("offset", 0)
		if token, err := ordinals.LoadFungible(tickId); err != nil {
			return &fiber.Error{
				Code:    fiber.StatusInternalServerError,
				Message: err.Error(),
			}
		} else if token == nil {
			return &fiber.Error{
				Code:    fiber.StatusNotFound,
				Message: "Not Found",
			}
		} else if outpoints, err := rdb.ZRevRangeByScore(c.Context(), "FSALE:"+tickId, &redis.ZRangeBy{
			Min:    "0",
			Max:    "+inf",
			Offset: int64(offset),
			Count:  int64(limit),
		}).Result(); err != nil {
			return &fiber.Error{
				Code:    fiber.StatusInternalServerError,
				Message: err.Error(),
			}
		} else if len(outpoints) == 0 {
			return c.JSON([]string{})
		} else if txos, err := ordinals.LoadFungibleTxos(outpoints); err != nil {
			return &fiber.Error{
				Code:    fiber.StatusInternalServerError,
				Message: err.Error(),
			}
		} else {
			response := &TokenTxosResponse{
				Token: token,
				Txos:  make([]*ordinals.FungibleTxo, 0, len(txos)),
			}
			for _, txo := range txos {
				if txo != nil {
					response.Txos = append(response.Txos, txo)
				}
			}
			return c.JSON(response)
		}
	})

	// app.Get("/v1/tokens/:tickId/holders", func(c *fiber.Ctx) error {
	// 	tickId := c.Params("tickId")
	// 	if unix, err := cache.HGet(c.Context(), "FHOLDCACHE:"+tickId, tickId).Int64(); err != nil {
	// 		return &fiber.Error{
	// 			Code:    fiber.StatusInternalServerError,
	// 			Message: err.Error(),
	// 		}
	// 	} else if time.Since(time.Unix(unix, 0)) > HOLDER_CACHE_TIME {
	// 		iter := rdb.ZScan(c.Context(), "FHOLD:"+tickId, 0, "", 0).Iterator()
	// 		for iter.Next(c.Context()) {

	// 		}

	// 	}

	// })
	log.Println("Listening on", PORT)
	app.Listen(fmt.Sprintf(":%d", PORT))
}

// HealthCheck godoc
// @Summary Show the status of server.
// @Description get the status of server.
// @Tags root
// @Accept */*
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Router / [get]
func HealthCheck(c *fiber.Ctx) error {
	res := map[string]interface{}{
		"data": "Server is up and running",
	}

	if err := c.JSON(res); err != nil {
		return err
	}

	return nil
}
