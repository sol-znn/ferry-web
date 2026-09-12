package sol

import (
	"encoding/binary"
	"fmt"
)

// The instruction and account layouts of the solzen-htlc program, mirroring
// program/src/lib.rs.
//
// Two implementations of one layout is one more than ideal and one fewer than
// the alternative: the page needs to build these instructions, and it needs to
// read an escrow back without trusting the page's own JavaScript. Keeping both
// in Go means the browser has a single source for what an escrow *is*, and the
// tests here check the encoding against a real account the deployed program
// wrote, so a drift between this file and the Rust one fails loudly.

const (
	tagCreate byte = 0
	tagRedeem byte = 1
	tagRefund byte = 2
)

// EscrowLen is the byte length of a serialized escrow account.
const EscrowLen = 146

// pdaPrefix is the escrow PDA's first seed.
const pdaPrefix = "htlc"

// MaxPreimage is the largest preimage the program will hash, and matches the
// keyMaxSize the Zenon leg is created with.
const MaxPreimage = 32

const (
	offTag       = 0
	offSwapID    = 1
	offInitiator = 33
	offReceiver  = 65
	offAmount    = 97
	offHashlock  = 105
	offTimelock  = 137
	offBump      = 145
)

// tagActive is the escrow's live marker.
const tagActive = 1

// Escrow is one live HTLC on Solana.
type Escrow struct {
	SwapID    [32]byte `json:"-"`
	Initiator Pubkey   `json:"initiator"`
	Receiver  Pubkey   `json:"receiver"`
	// Amount is what the receiver is owed, in lamports. The account holds more
	// than this -- it also holds its own rent, which goes back to the initiator
	// on either exit -- so an escrow's balance is not the swap amount and the
	// two must not be compared.
	Amount   uint64   `json:"amount"`
	Hashlock [32]byte `json:"-"`
	Timelock int64    `json:"timelock"`
	Bump     uint8    `json:"bump"`
	// Lamports is the account's whole balance, kept so a caller can show the
	// rent portion separately.
	Lamports uint64 `json:"lamports"`
}

// EscrowAddress derives the account an escrow with this id lives at.
func EscrowAddress(programID Pubkey, swapID [32]byte) (Pubkey, uint8, error) {
	return FindProgramAddress([][]byte{[]byte(pdaPrefix), swapID[:]}, programID)
}

// DecodeEscrow parses an escrow account's data.
//
// It refuses a wrong length or a wrong tag rather than reading past what it
// understands, because the caller reaches here with bytes from a node it was
// told to trust for availability and not for content.
func DecodeEscrow(data []byte, lamports uint64) (*Escrow, error) {
	if len(data) != EscrowLen {
		return nil, fmt.Errorf("an escrow account is %d bytes, this one is %d", EscrowLen, len(data))
	}
	if data[offTag] != tagActive {
		return nil, fmt.Errorf("this account is not a live escrow")
	}
	e := &Escrow{
		Amount:   binary.LittleEndian.Uint64(data[offAmount:offHashlock]),
		Timelock: int64(binary.LittleEndian.Uint64(data[offTimelock:offBump])),
		Bump:     data[offBump],
		Lamports: lamports,
	}
	copy(e.SwapID[:], data[offSwapID:offInitiator])
	copy(e.Initiator[:], data[offInitiator:offReceiver])
	copy(e.Receiver[:], data[offReceiver:offAmount])
	copy(e.Hashlock[:], data[offHashlock:offTimelock])
	return e, nil
}

// AccountMeta is one account an instruction touches, in the shape the
// JavaScript side turns into a web3.js AccountMeta.
type AccountMeta struct {
	Pubkey     string `json:"pubkey"`
	IsSigner   bool   `json:"isSigner"`
	IsWritable bool   `json:"isWritable"`
}

// Instruction is a ready-to-send instruction. The page assembles it into a
// transaction and hands that to a wallet; nothing about the instruction itself
// is decided in JavaScript.
type Instruction struct {
	ProgramID string        `json:"programId"`
	Accounts  []AccountMeta `json:"accounts"`
	// Data is base64, which is what survives JSON without a byte-array dance.
	Data string `json:"data"`
}

// CreateParams is one escrow to open.
type CreateParams struct {
	ProgramID Pubkey
	SwapID    [32]byte
	Initiator Pubkey
	Receiver  Pubkey
	Amount    uint64
	Hashlock  [32]byte
	Timelock  int64
}

// BuildCreate encodes the create instruction.
//
// The initiator is the only signer, and is also the refund destination: the
// program records the account that paid, so there is no parameter that could
// point the refund somewhere else.
func BuildCreate(p CreateParams) (*Instruction, error) {
	if p.Amount == 0 {
		return nil, fmt.Errorf("the amount must be positive")
	}
	escrow, _, err := EscrowAddress(p.ProgramID, p.SwapID)
	if err != nil {
		return nil, err
	}
	data := make([]byte, 0, 113)
	data = append(data, tagCreate)
	data = append(data, p.SwapID[:]...)
	data = append(data, p.Receiver[:]...)
	data = binary.LittleEndian.AppendUint64(data, p.Amount)
	data = append(data, p.Hashlock[:]...)
	data = binary.LittleEndian.AppendUint64(data, uint64(p.Timelock))

	return &Instruction{
		ProgramID: p.ProgramID.String(),
		Accounts: []AccountMeta{
			{Pubkey: p.Initiator.String(), IsSigner: true, IsWritable: true},
			{Pubkey: escrow.String(), IsWritable: true},
			{Pubkey: SystemProgram.String()},
		},
		Data: encodeBase64(data),
	}, nil
}

// BuildRedeem encodes the redeem instruction.
//
// No account here is a signer. The preimage is the authorization and the
// destinations came from the escrow, so whoever pays the transaction fee is the
// only party the wallet has to be -- which is what lets either side push a
// settlement the other has stopped watching for.
func BuildRedeem(programID Pubkey, escrowState *Escrow, escrow Pubkey, preimage []byte) (*Instruction, error) {
	if len(preimage) == 0 || len(preimage) > MaxPreimage {
		return nil, fmt.Errorf("the preimage must be 1..%d bytes, got %d", MaxPreimage, len(preimage))
	}
	data := make([]byte, 0, 2+len(preimage))
	data = append(data, tagRedeem, byte(len(preimage)))
	data = append(data, preimage...)

	return &Instruction{
		ProgramID: programID.String(),
		Accounts: []AccountMeta{
			{Pubkey: escrow.String(), IsWritable: true},
			{Pubkey: escrowState.Receiver.String(), IsWritable: true},
			{Pubkey: escrowState.Initiator.String(), IsWritable: true},
		},
		Data: encodeBase64(data),
	}, nil
}

// BuildRefund encodes the refund instruction, which is valid only once the
// escrow's timelock has passed and pays only the initiator.
func BuildRefund(programID Pubkey, escrowState *Escrow, escrow Pubkey) (*Instruction, error) {
	return &Instruction{
		ProgramID: programID.String(),
		Accounts: []AccountMeta{
			{Pubkey: escrow.String(), IsWritable: true},
			{Pubkey: escrowState.Initiator.String(), IsWritable: true},
		},
		Data: encodeBase64([]byte{tagRefund}),
	}, nil
}

// DecodeRedeemPreimage pulls the preimage out of a redeem instruction's data,
// and reports false for anything else -- a create or a refund on the same
// program, which is most of what a scan of an escrow's history will find.
//
// This is how the secret crosses chains when Solana is the leg the initiator
// claims: it is published, in the clear, in the instruction that spends the
// escrow, and the counterparty reads it from there.
func DecodeRedeemPreimage(data []byte) ([]byte, bool) {
	if len(data) < 2 || data[0] != tagRedeem {
		return nil, false
	}
	n := int(data[1])
	if n == 0 || n > MaxPreimage || len(data) != 2+n {
		return nil, false
	}
	return data[2 : 2+n], true
}
