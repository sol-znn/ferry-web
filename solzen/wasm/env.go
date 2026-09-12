package main

// BuildName identifies the build, set by a linker flag so that a page can say
// which module it loaded. It has no effect on behaviour.
var BuildName = "dev"

// SolProgramID is the deployed program this build defaults to. It is set at
// build time from program/target/deploy/solzen_htlc-keypair.json, because a
// program's address comes from its keypair and so changes whenever the program
// is deployed somewhere new.
//
// A default, not a constant: the page's settings can point at any deployment,
// and an offer that names a different one is refused rather than silently
// accepted, so a wrong default costs a message and not money.
var SolProgramID = ""

// DefaultConfig is what a browser with no saved settings starts from.
//
// Local endpoints, on purpose. The Solana leg would work against a public RPC,
// but the Zenon one needs a node that answers HTTP JSON-RPC on 35997 with CORS
// headers, and pointing a new user at a stranger's node for the one check where
// a dishonest answer costs money is the wrong default to ship.
func DefaultConfig() Config {
	return Config{
		SolanaURL:  "http://127.0.0.1:8899",
		ZenonURL:   "http://127.0.0.1:35997",
		SolProgram: SolProgramID,
	}
}
