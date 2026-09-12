package znn

import (
	"encoding/base64"
	"testing"
)

// block is a send to `to` carrying `data`, as the node would marshal it.
func block(hash, to string, data []byte, at int64) *AccountBlock {
	b := &AccountBlock{Hash: hash, ToAddress: to, Data: base64.StdEncoding.EncodeToString(data)}
	if at != 0 {
		b.ConfirmationDetail = &struct {
			MomentumHeight    uint64 `json:"momentumHeight"`
			MomentumTimestamp int64  `json:"momentumTimestamp"`
		}{MomentumHeight: 1, MomentumTimestamp: at}
	}
	return b
}

// An htlc.create's arguments are ABI-encoded, so the hashlock sits inside the
// data with a method id in front of it and other arguments around it. The
// search must find it there rather than only at an offset it happens to know.
func createData(hashLock []byte) []byte {
	data := []byte{0x1a, 0x2b, 0x3c, 0x4d} // method id
	data = append(data, make([]byte, 96)...)
	data = append(data, hashLock...)
	return append(data, make([]byte, 32)...)
}

func TestCollectCreatesFindsTheHashlock(t *testing.T) {
	lock := make([]byte, HashLockSize)
	for i := range lock {
		lock[i] = byte(i)
	}
	other := make([]byte, HashLockSize) // all zero: a different swap's hash

	blocks := []*AccountBlock{
		block("aa", HtlcContractAddress, createData(other), 100),
		block("bb", "z1qzsomewhereelsexxxxxxxxxxxxxxxxxxxxxxx", createData(lock), 200),
		block("cc", HtlcContractAddress, createData(lock), 300),
	}
	var got []HtlcCandidate
	for _, b := range blocks {
		got = collectCreates(b, lock, got)
	}
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1: %+v", len(got), got)
	}
	if got[0].ID != "cc" {
		t.Errorf("candidate is %q, want the create carrying this swap's hashlock", got[0].ID)
	}
	if got[0].At != 300 {
		t.Errorf("timestamp is %d, want 300", got[0].At)
	}
}

// A create sent as part of a batch is still a create, and its own hash is still
// the id the contract answers to -- so the parent's hash must not be reported
// in its place.
func TestCollectCreatesDescendsIntoBatches(t *testing.T) {
	lock := make([]byte, HashLockSize)
	lock[0] = 0xff

	parent := block("parent", "z1qzsomewhereelsexxxxxxxxxxxxxxxxxxxxxxx", nil, 10)
	parent.DescendantBlocks = []*AccountBlock{block("child", HtlcContractAddress, createData(lock), 10)}

	got := collectCreates(parent, lock, nil)
	if len(got) != 1 || got[0].ID != "child" {
		t.Fatalf("got %+v, want the descendant's own hash", got)
	}
}

func TestFindHtlcCreatesRejectsABadHashlock(t *testing.T) {
	c := New("wss://example:35998")
	if _, err := c.FindHtlcCreates(t.Context(), "z1qz…", []byte{1, 2, 3}, 1, 1); err == nil {
		t.Fatal("a short hashlock was accepted")
	}
	if _, err := c.FindHtlcCreates(t.Context(), "  ", make([]byte, HashLockSize), 1, 1); err == nil {
		t.Fatal("an empty creator address was accepted")
	}
}
