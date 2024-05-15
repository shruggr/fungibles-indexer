package lib

import (
	"encoding/json"

	"github.com/libsv/go-bt/bscript"
)

type PKHash []byte

func (p *PKHash) Address() (string, error) {
	add, err := bscript.NewAddressFromPublicKeyHash(*p, true)
	if err != nil {
		return "", err
	}
	return add.AddressString, nil
}

// MarshalJSON serializes ByteArray to hex
func (p PKHash) MarshalJSON() ([]byte, error) {
	add, err := p.Address()
	if err != nil {
		return nil, err
	}
	return json.Marshal(add)
}

func NewPKHashFromAddress(a string) (p *PKHash, err error) {
	add, err := bscript.NewAddressFromString(a)
	// script, err := bscript.NewP2PKHFromAddress(a)
	if err != nil {
		return
	}

	pkh := PKHash(add.PublicKeyHash)
	return &pkh, nil
}

func (p *PKHash) UnmarshalJSON(data []byte) error {
	var add string
	err := json.Unmarshal(data, &add)
	if err != nil {
		return err
	}
	if pkh, err := NewPKHashFromAddress(add); err != nil {
		return err
	} else {
		*p = *pkh
	}
	return nil
}
