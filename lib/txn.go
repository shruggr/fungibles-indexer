package lib

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"

	"github.com/libsv/go-bt/bscript"
	"github.com/libsv/go-bt/v2"
	"github.com/redis/go-redis/v9"
)

const THREADS = 64

type IndexContext struct {
	Tx      *bt.Tx     `json:"-"`
	Txid    ByteString `json:"txid"`
	BlockId *string    `json:"blockId"`
	Height  uint32     `json:"height"`
	Idx     uint64     `json:"idx"`
	Txos    []*Txo     `json:"txos"`
	Spends  []*Txo     `json:"spends"`
}

func (ctx *IndexContext) SaveTxos(cmdable redis.Cmdable) {
	for _, txo := range ctx.Txos {
		txo.Save(ctx, cmdable)
	}

}

func (ctx *IndexContext) SaveSpends(cmdable redis.Cmdable) {
	scoreHeight := ctx.Height
	if scoreHeight == 0 {
		scoreHeight = uint32(time.Now().Unix())
	}
	spentScore, err := strconv.ParseFloat(fmt.Sprintf("1.%010d", scoreHeight), 64)
	if err != nil {
		panic(err)
	}
	for _, spend := range ctx.Spends {
		spend.SetSpend(ctx, cmdable, spentScore)
	}
}

func IndexTxn(rawtx []byte, blockId string, height uint32, idx uint64) (ctx *IndexContext, err error) {
	ctx, err = ParseTxn(rawtx, blockId, height, idx)
	if err != nil {
		return
	}
	pipe := Rdb.Pipeline()

	ctx.SaveSpends(pipe)

	ctx.SaveTxos(pipe)
	score := ctx.Height
	if score == 0 {
		score = uint32(time.Now().Unix())
	}
	pipe.ZAddNX(context.Background(), "tx:log", redis.Z{
		Score:  float64(score),
		Member: hex.EncodeToString(ctx.Txid),
	})
	_, err = pipe.Exec(context.Background())
	return
}

func ParseTxn(rawtx []byte, blockId string, height uint32, idx uint64) (ctx *IndexContext, err error) {
	tx, err := bt.NewTxFromBytes(rawtx)
	if err != nil {
		panic(err)
	}
	txid := tx.TxIDBytes()
	ctx = &IndexContext{
		Tx:     tx,
		Txid:   txid,
		Spends: make([]*Txo, 0, len(tx.Inputs)),
		Txos:   make([]*Txo, 0, len(tx.Outputs)),
	}
	if height > 0 {
		ctx.BlockId = &blockId
		ctx.Height = height
		ctx.Idx = idx
	}

	if !tx.IsCoinbase() {
		ParseSpends(ctx)
	}

	ParseTxos(ctx)
	return
}

func ParseSpends(ctx *IndexContext) {
	inAcc := uint64(0)
	for vin, txin := range ctx.Tx.Inputs {
		outpoint := NewOutpoint(txin.PreviousTxID(), txin.PreviousTxOutIndex)
		spend, err := LoadTxo(outpoint.String())
		if err != nil {
			panic(err)
		}
		if spend == nil {
			if inTx, err := LoadTx(txin.PreviousTxIDStr()); err != nil {
				panic(err)
			} else {
				inCtx := &IndexContext{
					Tx:   inTx,
					Txid: inTx.TxIDBytes(),
				}
				ParseTxos(inCtx)
				spend = inCtx.Txos[txin.PreviousTxOutIndex]
			}
		}
		spend.Spend = &ctx.Txid
		spend.Vin = uint32(vin)
		spend.SpendHeight = ctx.Height
		spend.SpendIdx = ctx.Idx
		spend.InAcc = inAcc
		ctx.Spends = append(ctx.Spends, spend)
		inAcc += spend.Satoshis
	}
}

func ParseTxos(ctx *IndexContext) {
	height := ctx.Height
	if height == 0 {
		height = uint32(time.Now().Unix())
	}
	accSats := uint64(0)
	for vout, txout := range ctx.Tx.Outputs {
		outpoint := Outpoint(binary.BigEndian.AppendUint32(ctx.Txid, uint32(vout)))
		txo := &Txo{
			Height:   ctx.Height,
			Idx:      ctx.Idx,
			Satoshis: txout.Satoshis,
			OutAcc:   accSats,
			Outpoint: &outpoint,
			Script:   *txout.LockingScript,
		}

		if len(txo.Script) >= 25 && bscript.NewFromBytes(txo.Script[:25]).IsP2PKH() {
			pkhash := PKHash(txo.Script[3:23])
			txo.PKHash = &pkhash
		}

		ctx.Txos = append(ctx.Txos, txo)
		accSats += txout.Satoshis
	}
}

// var spendsCache = make(map[string][]*Txo)
// var m sync.Mutex

// func LoadSpends(txid ByteString, tx *bt.Tx) []*Txo {
// 	// fmt.Println("Loading Spends", hex.EncodeToString(txid))
// 	var err error
// 	if tx == nil {
// 		tx, err = LoadTx(hex.EncodeToString(txid))
// 		if err != nil {
// 			log.Panicf("[LoadSpends] %x %v\n", txid, err)
// 		}
// 	}

// 	outpoints := make([]string, len(tx.Inputs))
// 	for vin, txin := range tx.Inputs {
// 		outpoints[vin] = NewOutpoint(txin.PreviousTxID(), txin.PreviousTxOutIndex).String()
// 	}
// 	spendByOutpoint := make(map[string]*Txo, len(tx.Inputs))
// 	spends := make([]*Txo, 0, len(tx.Inputs))

// 	spends, err :=
// 	rows, err := Db.Query(context.Background(), `
// 		SELECT outpoint, satoshis, outacc
// 		FROM txos
// 		WHERE spend=$1`,
// 		txid,
// 	)
// 	if err != nil {
// 		log.Panic(err)
// 	}
// 	defer rows.Close()

// 	for rows.Next() {
// 		spend := &Txo{
// 			Spend: &txid,
// 		}
// 		var satoshis sql.NullInt64
// 		var outAcc sql.NullInt64
// 		err = rows.Scan(&spend.Outpoint, &satoshis, &outAcc)
// 		if err != nil {
// 			log.Panic(err)
// 		}
// 		if satoshis.Valid && outAcc.Valid {
// 			spend.Satoshis = uint64(satoshis.Int64)
// 			spend.OutAcc = uint64(outAcc.Int64)
// 			spendByOutpoint[spend.Outpoint.String()] = spend
// 		}
// 	}

// 	var inSats uint64
// 	for vin, txin := range tx.Inputs {
// 		outpoint := NewOutpoint(txin.PreviousTxID(), txin.PreviousTxOutIndex)
// 		spend, ok := spendByOutpoint[outpoint.String()]
// 		if !ok {
// 			spend = &Txo{
// 				Outpoint: outpoint,
// 				Spend:    &txid,
// 				Vin:      uint32(vin),
// 			}

// 			tx, err := LoadTx(txin.PreviousTxIDStr())
// 			if err != nil {
// 				log.Panic(txin.PreviousTxIDStr(), err)
// 			}
// 			var outSats uint64
// 			for vout, txout := range tx.Outputs {
// 				if vout < int(spend.Outpoint.Vout()) {
// 					outSats += txout.Satoshis
// 					continue
// 				}
// 				spend.Satoshis = txout.Satoshis
// 				spend.OutAcc = outSats
// 				spend.Save()
// 				spendByOutpoint[outpoint.String()] = spend
// 				break
// 			}
// 		} else {
// 			spend.Vin = uint32(vin)
// 		}
// 		spends = append(spends, spend)
// 		inSats += spend.Satoshis
// 		// fmt.Println("Inputs:", spends[vin].Outpoint)
// 	}
// 	return spends
// }
