// Command htlcvectors prints golden ABI encodings for the htlc contract, using
// go-zenon's own encoder.
//
// It lives here because this module already depends on go-zenon and ferry-web
// deliberately does not: ferry-web hand-rolls the three htlc encodings to keep
// go-ethereum out of a 2.8 MB wasm module, and pins them to this program's
// output so the hand-rolled version cannot drift from the chain's.
//
// It goes through this module's `znn` package rather than go-zenon's
// `vm/embedded/definition`, for the reason that package's own comment gives:
// `definition` reaches common/db reaches goleveldb, which does not build here.
// The ABI description is copied verbatim; the encoder is go-zenon's `vm/abi`.
package main

import (
	"encoding/hex"
	"fmt"

	"github.com/zenon-network/go-zenon/common/types"

	"github.com/zenon/solzen/wasm/znn"
)

func main() {
	hashLocked := types.ParseAddressPanic("z1qq6eg8n43g032hanpsfp02qcdmv7zfj3y2lt5d")
	lock, _ := hex.DecodeString("0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20")
	var id types.Hash
	idRaw, _ := hex.DecodeString("aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899")
	if err := id.SetBytes(idRaw); err != nil {
		panic(err)
	}
	preimage, _ := hex.DecodeString("202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f")

	for _, name := range []string{"Create", "Unlock", "Reclaim"} {
		method := znn.ABIHtlc.Methods[name]
		fmt.Printf("id %-8s %s  sig=%s\n", name, hex.EncodeToString(method.Id()), method.Sig())
	}

	create, err := znn.PackCreate(hashLocked, 1788700000, 1, 32, lock)
	fmt.Println("create", hex.EncodeToString(create), err)

	unlock, err := znn.PackUnlock(id, preimage)
	fmt.Println("unlock", hex.EncodeToString(unlock), err)

	reclaim, err := znn.PackReclaim(id)
	fmt.Println("reclaim", hex.EncodeToString(reclaim), err)

	fmt.Println("addrbytes", hex.EncodeToString(hashLocked.Bytes()))
	fmt.Println("htlcaddrbytes", hex.EncodeToString(types.HtlcContract.Bytes()))
}
