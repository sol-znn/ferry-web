package main

import (
	"fmt"
	"math/big"
	"strings"

	"github.com/zenon/ferry-web-v2/wasm/sol"
	"github.com/zenon/ferry-web-v2/wasm/znn"
)

// Which chains a leg can settle on, and what is true of each one regardless of
// which side of a trade it is.
//
// v1 of this app had one pair and so had no need for this file: "the Bitcoin
// leg" and "the Zenon leg" were two named fields, and every rule could be
// written against one or the other. Four pairs make that impossible — a
// ZTS↔ZTS swap has two Zenon legs and no Bitcoin one — so a leg now carries its
// chain and the rules are written against the pair.
//
// Nothing here decides anything about safety. It is the vocabulary: which
// chains exist, what their money is called, and which pairs this build will
// let somebody agree to.

// ChainID names a chain a leg can settle on.
type ChainID string

const (
	// ChainBTC is a P2SH hashed-timelock contract. Funded by an ordinary
	// payment; spent by an ephemeral key this app generates per swap.
	ChainBTC ChainID = "btc"
	// ChainZNN is go-zenon's `htlc` embedded contract, called with account
	// blocks the user's own wallet signs. Carries a ZTS, so two Zenon legs can
	// trade against each other as long as the tokens differ.
	ChainZNN ChainID = "znn"
	// ChainSOL is the solzen-htlc program: an escrow PDA holding lamports, with
	// both exits paying addresses fixed at creation.
	ChainSOL ChainID = "sol"
)

// Chain is what the rest of the code needs to know about a chain without
// caring which one it is.
type Chain struct {
	ID ChainID
	// Label is how a sentence names it: "Bitcoin", not "btc".
	Label string
	// Unit is what a person types amounts in — sat, ZNN-or-whatever-ZTS, SOL.
	// It is NOT always the base unit: Solana is typed in SOL and held in
	// lamports, which is exactly the conversion that has to happen in one place.
	Unit string
	// Decimals converts the typed unit into base units. Zero for Bitcoin,
	// because this app has always worked in satoshi. -1 for Zenon, whose
	// decimals belong to the token and are read from a node.
	Decimals int
	// Tokenised says a leg on this chain names a token as well as an amount.
	Tokenised bool
	// Proof is the scheme an address on this chain is claimed with on the board.
	Proof string
}

var chains = map[ChainID]Chain{
	ChainBTC: {ID: ChainBTC, Label: "Bitcoin", Unit: "sat", Decimals: 0, Proof: ProofBTC},
	ChainZNN: {ID: ChainZNN, Label: "Zenon", Unit: "", Decimals: -1, Tokenised: true, Proof: ProofZNN},
	ChainSOL: {ID: ChainSOL, Label: "Solana", Unit: "SOL", Decimals: 9, Proof: ProofSOL},
}

// ChainOrder is every chain, in the order sentences and lists name them. A
// fixed order so two sentences on the same page never disagree.
var ChainOrder = []ChainID{ChainBTC, ChainZNN, ChainSOL}

// KnownChain reports whether this build can settle a leg here.
func KnownChain(c ChainID) bool { _, ok := chains[c]; return ok }

// ChainOf returns a chain's properties, or an error naming what was asked for.
func ChainOf(c ChainID) (Chain, error) {
	got, ok := chains[c]
	if !ok {
		return Chain{}, fmt.Errorf("%q is not a chain this build knows: it is %s",
			c, strings.Join(chainIDStrings(), ", "))
	}
	return got, nil
}

// ChainLabel names a chain for a sentence, falling back to the raw id so a
// record from a newer build still renders something.
func ChainLabel(c ChainID) string {
	if got, ok := chains[c]; ok {
		return got.Label
	}
	return string(c)
}

func chainIDStrings() []string {
	out := make([]string, 0, len(ChainOrder))
	for _, c := range ChainOrder {
		out = append(out, string(c))
	}
	return out
}

// ---------- pairs ----------

// Pair is a trade this build will let two people agree to.
//
// A list rather than "any two chains", because two of the four combinations
// that would produce are not trades: BTC against BTC is a transfer with extra
// steps, and ZTS against the SAME ZTS is the same. The one that IS allowed and
// looks like it should not be — Zenon against Zenon — is allowed precisely
// because the tokens differ, which is checked separately (see Pair.Check).
type Pair struct {
	// A and B are the two chains, in the order the label names them. A trade
	// has no inherent direction: which side a given user is on is their leg's
	// Dir, not a property of the pair.
	A, B ChainID
	// Label is how the pair is offered: "BTC ⇄ ZNN".
	Label string
}

// Pairs is every trade this build supports.
var Pairs = []Pair{
	{A: ChainBTC, B: ChainZNN, Label: "BTC ⇄ ZTS"},
	{A: ChainZNN, B: ChainZNN, Label: "ZTS ⇄ ZTS"},
	{A: ChainSOL, B: ChainZNN, Label: "SOL ⇄ ZTS"},
	{A: ChainSOL, B: ChainBTC, Label: "SOL ⇄ BTC"},
}

// PairID is the stable name a pair travels under, in the order Pairs declares
// it — so "sol-btc" whichever side of it a given user is on. Derived from the
// chains rather than stored beside them, because a stored id is a second thing
// that can disagree with the legs it claims to summarise.
func PairID(a, b ChainID) string {
	for _, p := range Pairs {
		if (p.A == a && p.B == b) || (p.A == b && p.B == a) {
			return string(p.A) + "-" + string(p.B)
		}
	}
	return string(a) + "-" + string(b)
}

// SupportedPair reports whether these two chains may trade against each other.
func SupportedPair(a, b ChainID) bool {
	for _, p := range Pairs {
		if (p.A == a && p.B == b) || (p.A == b && p.B == a) {
			return true
		}
	}
	return false
}

// PairLabel names a pair for a button.
func PairLabel(a, b ChainID) string {
	for _, p := range Pairs {
		if (p.A == a && p.B == b) || (p.A == b && p.B == a) {
			return p.Label
		}
	}
	return ChainLabel(a) + " ⇄ " + ChainLabel(b)
}

// CheckPair refuses a combination that is not a trade.
//
// The two-Zenon case is the whole reason this takes tokens as well as chains. A
// swap is only atomic because two DIFFERENT things are locked against one
// secret; ZNN for ZNN through two HTLCs is an elaborate way to pay yourself, and
// worse, both halves verify perfectly, so nothing later would catch it.
func CheckPair(a, b ChainID, tokenA, tokenB string) error {
	if !KnownChain(a) {
		_, err := ChainOf(a)
		return err
	}
	if !KnownChain(b) {
		_, err := ChainOf(b)
		return err
	}
	if !SupportedPair(a, b) {
		return fmt.Errorf("this build does not swap %s against %s: it does %s",
			ChainLabel(a), ChainLabel(b), pairLabels())
	}
	if a == ChainZNN && b == ChainZNN {
		ta, tb := resolveToken(tokenA), resolveToken(tokenB)
		if ta == tb {
			return fmt.Errorf("both legs of this swap are %s, so there is nothing being "+
				"traded — a Zenon-to-Zenon swap needs two different tokens", ta)
		}
	}
	return nil
}

func pairLabels() string {
	out := make([]string, 0, len(Pairs))
	for _, p := range Pairs {
		out = append(out, p.Label)
	}
	return strings.Join(out, ", ")
}

// resolveToken applies the rule the creation form has always had: a blank token
// on a Zenon leg means ZNN.
func resolveToken(t string) string {
	t = strings.TrimSpace(t)
	if t == "" {
		return znn.ZnnTokenStandard
	}
	return t
}

// ---------- amounts ----------

// baseUnits converts a typed amount into a chain's base units.
//
// `decimals` is the chain's when it has fixed ones and the TOKEN's when the
// chain is Zenon, which is why it is a parameter rather than read from the map:
// a ZTS with 0 decimals and one with 8 are both ordinary, and getting it from
// anywhere but the issuing token is how a swap verifies against the wrong
// number.
func baseUnits(typed string, decimals int) (*big.Int, error) {
	return decimalToBaseUnits(strings.TrimSpace(typed), decimals)
}

// displayUnits is the inverse: base units back to the figure a person typed,
// trailing zeroes trimmed. Used only for display, never for a comparison.
func displayUnits(base *big.Int, decimals int) string {
	if decimals <= 0 {
		return base.String()
	}
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	whole, frac := new(big.Int).QuoRem(base, scale, new(big.Int))
	if frac.Sign() == 0 {
		return whole.String()
	}
	digits := strings.TrimRight(fmt.Sprintf("%0*s", decimals, frac.String()), "0")
	return whole.String() + "." + digits
}

// solLamports converts a typed SOL amount into lamports, refusing anything that
// will not fit — a uint64 of lamports is 18 billion SOL, so overflow means the
// figure is a mistake rather than a large trade.
func solLamports(typed string) (uint64, error) {
	n, err := baseUnits(typed, 9)
	if err != nil {
		return 0, fmt.Errorf("Solana amount: %w", err)
	}
	if n.Sign() <= 0 {
		return 0, fmt.Errorf("%q is not an amount of SOL", typed)
	}
	if !n.IsUint64() {
		return 0, fmt.Errorf("%q is more SOL than exists", typed)
	}
	return n.Uint64(), nil
}

// solDisplay renders lamports as SOL.
func solDisplay(lamports uint64) string {
	return displayUnits(new(big.Int).SetUint64(lamports), 9)
}

// LamportsPerSol is re-exported so callers do not import sol/ for one constant.
const LamportsPerSol = sol.LamportsPerSol
