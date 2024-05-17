package ord

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"log"
	"regexp"

	"github.com/shruggr/fungibles-indexer/lib"
	"github.com/shruggr/fungibles-indexer/mod/bitcom"
)

var AsciiRegexp = regexp.MustCompile(`^[[:ascii:]]*$`)

// func IndexTxn(rawtx []byte, blockId string, height uint32, idx uint64) (ctx *lib.IndexContext) {
// 	ctx, err := lib.ParseTxn(rawtx, blockId, height, idx)
// 	if err != nil {
// 		log.Panicln(err)
// 	}

// 	IndexInscriptions(ctx)
// 	return
// }

// func IndexInscriptions(ctx *lib.IndexContext) {
// 	CalculateOrigins(ctx)
// 	ParseInscriptions(ctx)
// 	ctx.SaveSpends()
// 	ctx.Save()

// 	lib.Db.Exec(context.Background(),
// 		`INSERT INTO txn_indexer(txid, indexer)
// 		VALUES ($1, 'ord')
// 		ON CONFLICT DO NOTHING`,
// 		ctx.Txid,
// 	)
// }

func CalculateOrigins(ctx *lib.IndexContext) {
	for _, txo := range ctx.Txos {
		if txo.Satoshis != 1 {
			continue
		}
		txo.Origin = LoadOrigin(txo.Outpoint, txo.OutAcc)
	}
}

func ParseInscriptions(ctx *lib.IndexContext) {
	for _, txo := range ctx.Txos {
		ParseScript(txo)
		bitcom.ParseScript(txo)
	}
}

func GetLatestOutpoint(ctx context.Context, origin *lib.Outpoint) (*lib.Outpoint, error) {
	var latest *lib.Outpoint

	// Update spends on all known unspent txos
	rows, err := lib.Db.Query(ctx, `
		SELECT outpoint
		FROM txos
		WHERE origin=$1 AND spend='\x'
		ORDER BY height DESC, idx DESC
		LIMIT 1`,
		origin,
	)
	if err != nil {
		// log.Println("FastForwardOrigin", err)
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var outpoint *lib.Outpoint
		err := rows.Scan(&outpoint)
		if err != nil {
			log.Println("FastForwardOrigin", err)
			return nil, err
		}

		spend, err := lib.JB.GetSpend(ctx, hex.EncodeToString(outpoint.Txid()), outpoint.Vout())
		if err != nil {
			log.Println("GetSpend", err)
			return nil, err
		}

		if len(spend) == 0 {
			latest = outpoint
			break
		}

		rawtx, err := lib.LoadRawtx(hex.EncodeToString(spend))
		if err != nil {
			log.Println("GetTransaction", err)
			return nil, err
		}
		if len(rawtx) < 100 {
			log.Println("transaction too short", string(rawtx))
			return nil, fmt.Errorf("transaction too short")
		}
		IndexTxn(rawtx, "", 0, 0)
	}

	if latest != nil {
		return latest, nil
	}

	// Fast-forward origin
	row := lib.Db.QueryRow(ctx, `
		SELECT outpoint
		FROM txos
		WHERE origin = $1
		ORDER BY CASE WHEN spend='\x' THEN 1 ELSE 0 END DESC, height DESC, idx DESC
		LIMIT 1`,
		origin,
	)
	err = row.Scan(&latest)
	if err != nil {
		log.Println("Lookup latest", err)
		return nil, err
	}

	for {
		spend, err := lib.JB.GetSpend(ctx, hex.EncodeToString(latest.Txid()), latest.Vout())
		if err != nil {
			log.Println("GetSpend", err)
			return nil, err
		}

		if len(spend) == 0 {
			return latest, nil
		}

		txn, err := lib.JB.GetTransaction(ctx, hex.EncodeToString(spend))
		// rawtx, err := lib.LoadRawtx(hex.EncodeToString(spend))
		if err != nil {
			log.Println("GetTransaction", err)
			return nil, err
		}

		// log.Printf("Indexing: %s\n", hex.EncodeToString(spend))
		txCtx := IndexTxn(txn.Transaction, txn.BlockHash.String(), txn.BlockHeight, txn.BlockIndex)
		for _, txo := range txCtx.Txos {
			if txo.Origin != nil && bytes.Equal(*txo.Origin, *origin) {
				latest = txo.Outpoint
				break
			}
		}

		if !bytes.Equal(latest.Txid(), txCtx.Txid) {
			return latest, nil
		}
	}
}
