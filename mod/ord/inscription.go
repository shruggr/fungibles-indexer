package ord

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"

	"github.com/redis/go-redis/v9"
	"github.com/shruggr/fungibles-indexer/lib"
)

type Inscription struct {
	lib.Indexable
	Json      json.RawMessage        `json:"json,omitempty"`
	Text      string                 `json:"text,omitempty"`
	Words     []string               `json:"words,omitempty"`
	File      *lib.File              `json:"file,omitempty"`
	Pointer   *uint64                `json:"pointer,omitempty"`
	Parent    *lib.Outpoint          `json:"parent,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	Metaproto []byte                 `json:"metaproto,omitempty"`
	Fields    map[string]interface{} `json:"-"`
}

func (i *Inscription) Save(txCtx *lib.IndexContext, cmdable redis.Cmdable, txo *lib.Txo) {

}

func SetInscriptionNum(height uint32) (err error) {
	row := lib.Db.QueryRow(context.Background(),
		"SELECT MAX(num) FROM inscriptions",
	)
	var dbNum sql.NullInt64
	err = row.Scan(&dbNum)
	if err != nil {
		log.Panic(err)
		return
	}
	var num uint64
	if dbNum.Valid {
		num = uint64(dbNum.Int64 + 1)
	}

	rows, err := lib.Db.Query(context.Background(), `
		SELECT outpoint
		FROM inscriptions
		WHERE num = -1 AND height <= $1 AND height IS NOT NULL
		ORDER BY height, idx
		LIMIT 100000`,
		height,
	)
	if err != nil {
		log.Panic(err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		outpoint := &lib.Outpoint{}
		err = rows.Scan(&outpoint)
		if err != nil {
			log.Panic(err)
			return
		}
		// fmt.Printf("Inscription Num %d %d %s\n", num, height, outpoint)
		_, err = lib.Db.Exec(context.Background(), `
			UPDATE inscriptions
			SET num=$2
			WHERE outpoint=$1`,
			outpoint, num,
		)
		if err != nil {
			log.Panic(err)
			return
		}
		num++
	}
	lib.Rdb.Publish(context.Background(), "inscriptionNum", fmt.Sprintf("%d", num))
	// log.Println("Height", height, "Max Origin Num", num)
	return
}
