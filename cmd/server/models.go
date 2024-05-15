package main

import (
	"github.com/shruggr/fungibles-indexer/lib"
	"github.com/shruggr/fungibles-indexer/ordinals"
)

type TokenBalanceResponse struct {
	Tick     *string       `json:"tick,omitempty"`
	Id       *lib.Outpoint `json:"id,omitempty"`
	Symbol   *string       `json:"sym,omitempty"`
	Decimals uint8         `json:"dec,omitempty"`
	Icon     *lib.Outpoint `json:"icon,omitempty"`
	All      struct {
		Confirmed uint64 `json:"confirmed"`
		Pending   uint64 `json:"pending"`
	} `json:"all"`
	Listed struct {
		Confirmed uint64 `json:"confirmed"`
		Pending   uint64 `json:"pending"`
	} `json:"listed"`
}

type TokenResponse struct {
	ordinals.Fungible
	Total      int64  `json:"fundTotal"`
	Used       int64  `json:"fundUsed"`
	PendingOps uint32 `json:"pendingOps"`
	Pending    uint64 `json:"pending"`
	Included   bool   `json:"included"`
}

type TokenTxosResponse struct {
	Token *ordinals.Fungible      `json:"token,omitempty"`
	Txos  []*ordinals.FungibleTxo `json:"txos,omitempty"`
}
