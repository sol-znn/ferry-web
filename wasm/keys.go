package main

import (
	"encoding/hex"
	"fmt"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
)

// SwapKey is an ephemeral keypair generated for a single swap. It is never a
// key from the user's wallet: the user's wallet only ever performs an ordinary
// send to the contract address, so their wallet keys are never exposed to this
// service.
//
// The private key is required to move funds out of a contract (redeem or
// refund), which is why it is persisted with the swap and included in the
// recovery file.
type SwapKey struct {
	Priv []byte `json:"priv"` // 32-byte secp256k1 scalar
	Pub  []byte `json:"pub"`  // 33-byte compressed pubkey
	PKH  []byte `json:"pkh"`  // hash160(pub), the value embedded in the contract
}

// NewSwapKey generates a fresh ephemeral keypair.
func NewSwapKey() (*SwapKey, error) {
	priv, err := btcec.NewPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	pub := priv.PubKey().SerializeCompressed()
	return &SwapKey{
		Priv: priv.Serialize(),
		Pub:  pub,
		PKH:  btcutil.Hash160(pub),
	}, nil
}

// PrivKey rehydrates the secp256k1 private key for signing.
func (k *SwapKey) PrivKey() *btcec.PrivateKey {
	priv, _ := btcec.PrivKeyFromBytes(k.Priv)
	return priv
}

// WIF exports the private key in wallet-import format. This is what lets a
// user rescue a stuck swap with a third-party tool: import the WIF, and the
// contract can be spent with any software that can build a custom-script
// spend.
func (k *SwapKey) WIF(params *chaincfg.Params) (string, error) {
	priv, _ := btcec.PrivKeyFromBytes(k.Priv)
	wif, err := btcutil.NewWIF(priv, params, true)
	if err != nil {
		return "", err
	}
	return wif.String(), nil
}

// PubHex is a display helper.
func (k *SwapKey) PubHex() string { return hex.EncodeToString(k.Pub) }

// PKHHex is a display helper.
func (k *SwapKey) PKHHex() string { return hex.EncodeToString(k.PKH) }
