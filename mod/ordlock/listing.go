package ordlock

import (
	"github.com/redis/go-redis/v9"
	"github.com/shruggr/fungibles-indexer/lib"
)

type Listing struct {
	lib.Indexable
	PKHash   lib.PKHash `json:"-"`
	Price    uint64     `json:"price"`
	PayOut   []byte     `json:"payout"`
	PricePer float64    `json:"pricePer"`
	Sale     bool       `json:"sale,omitempty"`
}

func (l *Listing) Tag() string {
	return "list"
}

// func (l *Listing) Map() (m map[string]interface{}, err error) {
// 	if m, err = l.Txo.Map(); err != nil {
// 		return
// 	}
// 	m["price"] = l.Price
// 	m["payout"] = l.PayOut
// 	m["pricePer"] = l.PricePer
// 	m["sale"] = l.Sale
// 	return
// }

func (l *Listing) Save(ctx *lib.IndexContext, cmdable redis.Cmdable, txo *lib.Txo)     {}
func (l *Listing) SetSpend(ctx *lib.IndexContext, cmdable redis.Cmdable, txo *lib.Txo) {}
