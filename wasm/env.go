package main

// Which instance this module was built for.
//
// Two deployments exist and they are identical in every way a user can see. The
// only differences are which chains they default to and which nodes they expect,
// and those live in settings the user can change -- so nothing about a running
// page would say which build it came from. That is a bad property for software
// that signs Bitcoin transactions: somebody testing a refund path against
// regtest and somebody moving real money are one bookmark apart.
//
// So the instance is decided once, at build time, and baked into the artefact:
//
//	go build -ldflags "-X main.BuildEnv=dev"     scripts/build.mjs --dev
//
// A linker flag rather than a runtime setting on purpose: a flag the page could
// flip would be one it could be tricked into flipping. The same value is handed
// to Vite in the same step, so the Go half and the JavaScript half of one build
// can never disagree, and scripts/smoke.mjs asserts the module reports the env
// the build asked for.
//
// prod is the default because it is the safe way round to be wrong: an
// unrecognised value produces a build that says "prod" and defaults to mainnet,
// which is conspicuous, rather than one that quietly files mainnet swaps under a
// development namespace.
var BuildEnv = EnvProd

// The two instances. There is no third, and no "staging" that is really prod
// with a different hostname — a swap either runs against chains where coins are
// worth something or it does not.
const (
	EnvDev  = "dev"
	EnvProd = "prod"
)

// Env is the instance this build is, normalised. Anything that is not exactly
// "dev" is prod; see the note on BuildEnv for why that direction.
func Env() string {
	if BuildEnv == EnvDev {
		return EnvDev
	}
	return EnvProd
}

// IsDev reports whether this is the development instance.
func IsDev() bool { return Env() == EnvDev }
