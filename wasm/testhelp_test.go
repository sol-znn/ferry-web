package main

import (
	"testing"

	"github.com/zenon/ferry-web-v2/wasm/znn"
)

// Builders for the four pairs, so a test that is about one rule does not spend
// twenty lines describing a trade.
//
// They exist because v2's create takes two legs rather than a leg and an amount,
// and most tests care about exactly one field of that. Each returns the params a
// swap of that shape needs, ready to have the one interesting field overwritten.

const (
	znnSelfAddr  = "z1qqjnwjjpnue8xmmpanz6csze6tcmtzzdtfsww7"
	znnPeerAddr  = "z1qzal6c5s9rjnnxd2z7dvdhjxpmmj4fmw56a0mz"
	solSelfAddr  = "9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM"
	solPeerAddr  = "3Nq4mTLbF6MW9wCPXfXCJvUJqpKrTMRPmoTNqUEQJFYU"
	solProgramID = "Ha1cRuMYS3vtDrRxx1RcLNRHzcJUeYbYAJBqLsFwFvbe"
	solSwapIDHex = "9d2e1a0b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7"
)

// btcZnn is the pair v1 had: this user sends satoshi and receives a ZTS.
func btcZnn(role Role) CreateParams {
	return CreateParams{
		Role: role,
		Out: CreateLeg{
			Chain:    ChainBTC,
			Amount:   "400000",
			SelfAddr: regtestDest,
		},
		In: CreateLeg{
			Chain:    ChainZNN,
			Amount:   "10",
			SelfAddr: znnSelfAddr,
			PeerAddr: znnPeerAddr,
		},
	}
}

// znnBtc is the same trade from the other side: this user funds the Zenon leg.
func znnBtc(role Role) CreateParams {
	return CreateParams{
		Role: role,
		Out: CreateLeg{
			Chain:    ChainZNN,
			Amount:   "10",
			SelfAddr: znnSelfAddr,
			PeerAddr: znnPeerAddr,
		},
		In: CreateLeg{
			Chain:    ChainBTC,
			Amount:   "400000",
			SelfAddr: regtestDest,
		},
	}
}

// znnZnn is two Zenon legs against each other. The tokens must differ, which is
// the one rule this pair adds.
func znnZnn(role Role) CreateParams {
	return CreateParams{
		Role: role,
		Out: CreateLeg{
			Chain:    ChainZNN,
			Token:    znn.ZnnTokenStandard,
			Amount:   "10",
			SelfAddr: znnSelfAddr,
			PeerAddr: znnPeerAddr,
		},
		In: CreateLeg{
			Chain:    ChainZNN,
			Token:    QsrTokenStandard,
			Amount:   "100",
			SelfAddr: znnSelfAddr,
			PeerAddr: znnPeerAddr,
		},
	}
}

// solZnn sends SOL and receives a ZTS.
func solZnn(role Role) CreateParams {
	return CreateParams{
		Role: role,
		Out: CreateLeg{
			Chain:    ChainSOL,
			Amount:   "1.5",
			SelfAddr: solSelfAddr,
			PeerAddr: solPeerAddr,
			Program:  solProgramID,
			SwapID:   solSwapIDHex,
		},
		In: CreateLeg{
			Chain:    ChainZNN,
			Amount:   "10",
			SelfAddr: znnSelfAddr,
			PeerAddr: znnPeerAddr,
		},
	}
}

// solBtc sends SOL and receives satoshi.
func solBtc(role Role) CreateParams {
	return CreateParams{
		Role: role,
		Out: CreateLeg{
			Chain:    ChainSOL,
			Amount:   "1.5",
			SelfAddr: solSelfAddr,
			PeerAddr: solPeerAddr,
			Program:  solProgramID,
			SwapID:   solSwapIDHex,
		},
		In: CreateLeg{
			Chain:    ChainBTC,
			Amount:   "400000",
			SelfAddr: regtestDest,
		},
	}
}

// mustCreate builds a swap or fails the test.
func mustCreate(t *testing.T, m *Manager, p CreateParams) *Swap {
	t.Helper()
	sw, err := m.Create(p)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return sw
}
