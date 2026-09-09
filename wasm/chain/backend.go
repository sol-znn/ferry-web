package chain

import "context"

// Backend is the Bitcoin chain data source.
//
// Only one implementation is reachable from a browser. Bitcoin Core's JSON-RPC
// sends no CORS headers, expects HTTP Basic credentials a page cannot safely
// hold, and the call funding detection needs (`scantxoutset`) walks the entire
// UTXO set. So the interface stays and Esplora is the only implementation;
// privacy is recovered by pointing it at *your own* Esplora instance, which is a
// URL swap rather than a different protocol.
//
// The interface is never given a private key. It is read-only apart from
// Broadcast, which publishes a transaction already signed in WebAssembly with an
// ephemeral swap key.
type Backend interface {
	// TipHeight returns the current best block height.
	TipHeight(ctx context.Context) (int64, error)

	// AddressUTXOs returns unspent outputs paying to addr. This is how funding
	// is detected: the user pays the contract address from any wallet, and the
	// app watches for the resulting output.
	AddressUTXOs(ctx context.Context, addr string) ([]UTXO, error)

	// RawTx returns a serialized transaction as hex.
	RawTx(ctx context.Context, txid string) (string, error)

	// TxStatus reports whether a transaction is mined and in which block. It
	// is what turns "the funding arrived" into "the funding is n blocks deep",
	// which is the only thing that tells the user whether it is safe to act on.
	TxStatus(ctx context.Context, txid string) (Status, error)

	// OutspendOf reports whether an output has been spent and by which
	// transaction. Locating the spending transaction is what allows the secret
	// to be extracted from a counterparty's redeem.
	OutspendOf(ctx context.Context, txid string, vout uint32) (*Outspend, error)

	// FeeRate returns a sat/vB estimate for the given confirmation target.
	FeeRate(ctx context.Context, blocks int) (float64, error)

	// BlockHashAt returns the hash of the block at a height. It is what lets
	// two parties prove they are on the same chain rather than merely on
	// chains with the same name: everybody's regtest shares a genesis, so only
	// a block they have both already seen can tell one from another.
	BlockHashAt(ctx context.Context, height int64) (string, error)

	// Broadcast publishes a signed raw transaction and returns its txid.
	Broadcast(ctx context.Context, rawHex string) (string, error)

	// Name identifies the backend in the UI, so the user can see which data
	// source their swap is trusting.
	Name() string
}

// Compile-time check that the Esplora client satisfies the interface.
var _ Backend = (*Client)(nil)

// Build constructs a Backend from an explicit Esplora URL, or returns nil if
// none was given.
//
// nil is load-bearing: every Manager method that accepts an override reads it
// as "use the configured default". Returning a non-nil client wrapping an empty
// URL here would silently point every call at a broken backend.
func Build(esplora string) Backend {
	if esplora != "" {
		return New(esplora)
	}
	return nil
}
