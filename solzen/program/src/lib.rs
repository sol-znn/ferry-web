//! A hashed timelock contract for Solana, sized for one job: being the Solana
//! leg of a cross-chain atomic swap whose other leg is Zenon's `htlc` embedded
//! contract.
//!
//! Bitcoin needs no program for this -- a P2SH script *is* the contract, so
//! ferry's Bitcoin leg is funded by an ordinary payment. Solana has no script,
//! so the contract has to be a deployed program. That is what every Solana
//! atomic-swap system does, and it is the only structural difference between
//! this leg and ferry's Bitcoin one.
//!
//! Two properties are load-bearing, and both mirror the Zenon contract:
//!
//! * **Both branches pay a pre-committed address.** `redeem` pays the receiver
//!   named at creation; `refund` pays the initiator. No instruction takes a
//!   destination, so no caller -- including whoever runs the web page -- can
//!   redirect the money.
//! * **Neither branch requires a signature from the party being paid.** Anyone
//!   who knows the preimage can push a redeem, and anyone at all can push a
//!   refund once the timelock has passed. Zenon's htlc contract allows the same
//!   thing by default (`htlcProxyUnlockInfo`), which is what lets a swap settle
//!   without either wallet having to understand HTLCs. It also means a stuck
//!   swap can be finished by the counterparty rather than deadlocking.
//!
//! The preimage travels in `redeem`'s instruction data, so it lands on chain in
//! plain sight. That is not incidental: when Solana is the leg the initiator
//! claims, this transaction is the only place the counterparty can learn the
//! secret they need for the Zenon side.

use solana_program::{
    account_info::{next_account_info, AccountInfo},
    clock::Clock,
    entrypoint,
    entrypoint::ProgramResult,
    hash::hashv,
    msg,
    program::{invoke, invoke_signed},
    program_error::ProgramError,
    pubkey::Pubkey,
    rent::Rent,
    sysvar::Sysvar,
};
use solana_system_interface::{instruction as system_instruction, program as system_program};

entrypoint!(process_instruction);

/// Seed prefix for the escrow PDA. The full seeds are `[PDA_PREFIX, swap_id]`,
/// so the address is a pure function of a 32-byte id the client picks. The id
/// is *not* the hashlock: two swaps may legitimately commit to the same hash
/// (a retry, or a secret reused across a batch), and colliding on the escrow
/// address would make the second one unfundable.
const PDA_PREFIX: &[u8] = b"htlc";

/// Byte length of a serialized escrow account.
const ESCROW_LEN: usize = 146;

/// The largest preimage this program will hash. Zenon's htlc contract carries
/// its own `keyMaxSize`, set to 32 for a swap of this shape; matching it here
/// keeps a preimage that one side accepts from being one the other side
/// rejects.
const MAX_PREIMAGE: usize = 32;

/// Field offsets inside the escrow account. Hand-serialized rather than borsh:
/// the layout is fixed, tiny, and read by a non-Rust client, so writing it out
/// is cheaper than agreeing on a serialization library across two languages.
mod offset {
    pub const TAG: usize = 0;
    pub const SWAP_ID: usize = 1;
    pub const INITIATOR: usize = 33;
    pub const RECEIVER: usize = 65;
    pub const AMOUNT: usize = 97;
    pub const HASHLOCK: usize = 105;
    pub const TIMELOCK: usize = 137;
    pub const BUMP: usize = 145;
}

/// Value of the tag byte for a live escrow. A closed one has zero lamports and
/// is collected by the runtime, so this exists to reject *other* programs' data
/// and half-written accounts, not to track a lifecycle.
const TAG_ACTIVE: u8 = 1;

pub fn process_instruction(
    program_id: &Pubkey,
    accounts: &[AccountInfo],
    data: &[u8],
) -> ProgramResult {
    let (tag, rest) = data.split_first().ok_or(ProgramError::InvalidInstructionData)?;
    match tag {
        0 => create(program_id, accounts, rest),
        1 => redeem(program_id, accounts, rest),
        2 => refund(program_id, accounts, rest),
        _ => Err(ProgramError::InvalidInstructionData),
    }
}

/// `create` -- lock `amount` lamports until either the preimage shows up or the
/// timelock passes.
///
/// data:     swap_id[32] receiver[32] amount:u64 hashlock[32] timelock:i64
/// accounts: 0 initiator (signer, writable) -- pays, and is refunded on timeout
///           1 escrow PDA (writable)
///           2 system program
fn create(program_id: &Pubkey, accounts: &[AccountInfo], data: &[u8]) -> ProgramResult {
    if data.len() != 112 {
        return Err(ProgramError::InvalidInstructionData);
    }
    let swap_id: [u8; 32] = data[0..32].try_into().unwrap();
    let receiver = Pubkey::new_from_array(data[32..64].try_into().unwrap());
    let amount = u64::from_le_bytes(data[64..72].try_into().unwrap());
    let hashlock: [u8; 32] = data[72..104].try_into().unwrap();
    let timelock = i64::from_le_bytes(data[104..112].try_into().unwrap());

    let it = &mut accounts.iter();
    let initiator = next_account_info(it)?;
    let escrow = next_account_info(it)?;
    let system_program = next_account_info(it)?;

    if !initiator.is_signer {
        return Err(ProgramError::MissingRequiredSignature);
    }
    if *system_program.key != system_program::ID {
        return Err(ProgramError::IncorrectProgramId);
    }
    if amount == 0 {
        msg!("solzen: amount must be non-zero");
        return Err(ProgramError::InvalidArgument);
    }

    // A timelock already in the past would create an escrow that is refundable
    // in the same slot it is funded, which is not a contract -- it is a gift
    // with extra steps. The counterparty's own verification would catch it, but
    // refusing here means the money never moves in the first place.
    let now = Clock::get()?.unix_timestamp;
    if timelock <= now {
        msg!("solzen: timelock {} is not in the future (now {})", timelock, now);
        return Err(ProgramError::InvalidArgument);
    }

    let (expected, bump) = Pubkey::find_program_address(&[PDA_PREFIX, &swap_id], program_id);
    if expected != *escrow.key {
        msg!("solzen: escrow account is not the PDA for this swap id");
        return Err(ProgramError::InvalidArgument);
    }
    // An escrow that already has data is one this program wrote, so the swap id
    // really is taken. Lamports alone are not: see below.
    if !escrow.data_is_empty() || escrow.owner != &system_program::ID {
        msg!("solzen: swap id is already in use");
        return Err(ProgramError::AccountAlreadyInitialized);
    }

    // The escrow holds the swapped amount *and* its own rent. Rent is the
    // initiator's deposit, not part of the trade, so both exit paths return it
    // to them rather than folding it into what the receiver is owed.
    let rent = Rent::get()?.minimum_balance(ESCROW_LEN);
    let lamports = rent
        .checked_add(amount)
        .ok_or(ProgramError::ArithmeticOverflow)?;

    let seeds: &[&[u8]] = &[PDA_PREFIX, &swap_id, &[bump]];

    // `create_account` fails outright on an account that already holds
    // lamports, and the escrow's address is a pure function of a swap id that
    // travels in the offer string -- so anybody who has seen the offer can
    // compute this address and send one lamport to it, and the swap can then
    // never be funded. Nothing is stolen by that, but a trade dies for the
    // price of a lamport and the error blames the id.
    //
    // So the account is opened the long way when it has to be: top up what is
    // already there, then allocate and assign. The PDA signs for itself in
    // both, and the balance the griefer contributed simply becomes part of the
    // rent the initiator would have paid anyway.
    if escrow.lamports() > 0 {
        if let Some(short) = lamports.checked_sub(escrow.lamports()) {
            if short > 0 {
                invoke(
                    &system_instruction::transfer(initiator.key, escrow.key, short),
                    &[initiator.clone(), escrow.clone(), system_program.clone()],
                )?;
            }
        }
        invoke_signed(
            &system_instruction::allocate(escrow.key, ESCROW_LEN as u64),
            &[escrow.clone(), system_program.clone()],
            &[seeds],
        )?;
        invoke_signed(
            &system_instruction::assign(escrow.key, program_id),
            &[escrow.clone(), system_program.clone()],
            &[seeds],
        )?;
    } else {
        invoke_signed(
            &system_instruction::create_account(
                initiator.key,
                escrow.key,
                lamports,
                ESCROW_LEN as u64,
                program_id,
            ),
            &[initiator.clone(), escrow.clone(), system_program.clone()],
            &[seeds],
        )?;
    }

    // Whichever route was taken, the account must end up holding at least what
    // this escrow promises. A prefunded PDA that already held more than the
    // rent plus the amount would skip the transfer above; the surplus is
    // treated as part of the rent deposit and goes back to the initiator on
    // either exit, which is the same place the rest of it goes.
    if escrow.lamports() < lamports {
        msg!("solzen: the escrow does not hold the amount plus rent");
        return Err(ProgramError::InsufficientFunds);
    }

    let mut d = escrow.try_borrow_mut_data()?;
    d[offset::TAG] = TAG_ACTIVE;
    d[offset::SWAP_ID..offset::INITIATOR].copy_from_slice(&swap_id);
    d[offset::INITIATOR..offset::RECEIVER].copy_from_slice(initiator.key.as_ref());
    d[offset::RECEIVER..offset::AMOUNT].copy_from_slice(receiver.as_ref());
    d[offset::AMOUNT..offset::HASHLOCK].copy_from_slice(&amount.to_le_bytes());
    d[offset::HASHLOCK..offset::TIMELOCK].copy_from_slice(&hashlock);
    d[offset::TIMELOCK..offset::BUMP].copy_from_slice(&timelock.to_le_bytes());
    d[offset::BUMP] = bump;

    msg!("solzen: created, amount {} timelock {}", amount, timelock);
    Ok(())
}

/// `redeem` -- pay the receiver, given the preimage, before the timelock.
///
/// Deliberately permissionless: the receiver does not sign. The preimage is the
/// authorization, and the destination was fixed at creation, so a third party
/// pushing this transaction can only complete the swap the two parties agreed
/// to. The fee payer is whoever submits it.
///
/// data:     preimage_len:u8 preimage[..]
/// accounts: 0 escrow PDA (writable)
///           1 receiver (writable)   -- must equal the escrow's receiver
///           2 initiator (writable)  -- rent goes back here
fn redeem(program_id: &Pubkey, accounts: &[AccountInfo], data: &[u8]) -> ProgramResult {
    let (len, tail) = data.split_first().ok_or(ProgramError::InvalidInstructionData)?;
    let len = *len as usize;
    // An empty preimage is refused rather than hashed. No secret this system
    // produces is empty, so nothing is lost -- but the client's own decoder
    // (`sol.DecodeRedeemPreimage`) does not recognise a zero-length redeem, and
    // an instruction the program accepts and the client cannot read back is a
    // settlement the counterparty's scan would look straight past. The two
    // implementations of this layout have to agree about what a redeem is.
    if len == 0 || len > MAX_PREIMAGE || tail.len() != len {
        return Err(ProgramError::InvalidInstructionData);
    }
    let preimage = tail;

    let it = &mut accounts.iter();
    let escrow = next_account_info(it)?;
    let receiver = next_account_info(it)?;
    let initiator = next_account_info(it)?;

    let state = load(program_id, escrow)?;
    if *receiver.key != state.receiver {
        msg!("solzen: receiver account does not match the escrow");
        return Err(ProgramError::InvalidArgument);
    }
    if *initiator.key != state.initiator {
        msg!("solzen: initiator account does not match the escrow");
        return Err(ProgramError::InvalidArgument);
    }

    // Expiry is checked against the same clock the refund branch uses, so the
    // two branches cannot both be open: at any instant exactly one of them is.
    let now = Clock::get()?.unix_timestamp;
    if now >= state.timelock {
        msg!("solzen: expired at {} (now {})", state.timelock, now);
        return Err(ProgramError::InvalidArgument);
    }

    // SHA-256, because Zenon's htlc contract offers SHA3-256 and SHA-256 and
    // only the latter is a hash both chains can commit to. `hashv` is the
    // sol_sha256 syscall.
    if hashv(&[preimage]).to_bytes() != state.hashlock {
        msg!("solzen: preimage does not match the hashlock");
        return Err(ProgramError::InvalidArgument);
    }

    close_to(escrow, state.amount, receiver, initiator)?;
    msg!("solzen: redeemed {} lamports", state.amount);
    Ok(())
}

/// `refund` -- return everything to the initiator once the timelock has passed.
///
/// Also permissionless. There is nothing to authorize: the destination is the
/// initiator recorded at creation, and the only precondition is a clock reading
/// anyone can verify.
///
/// accounts: 0 escrow PDA (writable)
///           1 initiator (writable)
fn refund(program_id: &Pubkey, accounts: &[AccountInfo], data: &[u8]) -> ProgramResult {
    if !data.is_empty() {
        return Err(ProgramError::InvalidInstructionData);
    }
    let it = &mut accounts.iter();
    let escrow = next_account_info(it)?;
    let initiator = next_account_info(it)?;

    let state = load(program_id, escrow)?;
    if *initiator.key != state.initiator {
        msg!("solzen: initiator account does not match the escrow");
        return Err(ProgramError::InvalidArgument);
    }

    let now = Clock::get()?.unix_timestamp;
    if now < state.timelock {
        msg!("solzen: not refundable until {} (now {})", state.timelock, now);
        return Err(ProgramError::InvalidArgument);
    }

    close_to(escrow, 0, initiator, initiator)?;
    msg!("solzen: refunded {} lamports", state.amount);
    Ok(())
}

struct Escrow {
    initiator: Pubkey,
    receiver: Pubkey,
    amount: u64,
    hashlock: [u8; 32],
    timelock: i64,
}

/// Reads an escrow, refusing anything that is not one this program wrote.
///
/// The PDA is re-derived from the id in the account's own data rather than
/// trusted from the caller. Ownership alone would already prevent forgery, but
/// re-deriving also rules out a caller substituting one real escrow for
/// another, which is the mistake that costs money rather than the one that
/// merely errors.
fn load(program_id: &Pubkey, escrow: &AccountInfo) -> Result<Escrow, ProgramError> {
    if escrow.owner != program_id {
        return Err(ProgramError::IllegalOwner);
    }
    let d = escrow.try_borrow_data()?;
    if d.len() != ESCROW_LEN || d[offset::TAG] != TAG_ACTIVE {
        return Err(ProgramError::UninitializedAccount);
    }
    let swap_id: [u8; 32] = d[offset::SWAP_ID..offset::INITIATOR].try_into().unwrap();
    let bump = d[offset::BUMP];
    let expected = Pubkey::create_program_address(&[PDA_PREFIX, &swap_id, &[bump]], program_id)
        .map_err(|_| ProgramError::InvalidSeeds)?;
    if expected != *escrow.key {
        return Err(ProgramError::InvalidSeeds);
    }
    Ok(Escrow {
        initiator: Pubkey::new_from_array(
            d[offset::INITIATOR..offset::RECEIVER].try_into().unwrap(),
        ),
        receiver: Pubkey::new_from_array(d[offset::RECEIVER..offset::AMOUNT].try_into().unwrap()),
        amount: u64::from_le_bytes(d[offset::AMOUNT..offset::HASHLOCK].try_into().unwrap()),
        hashlock: d[offset::HASHLOCK..offset::TIMELOCK].try_into().unwrap(),
        timelock: i64::from_le_bytes(d[offset::TIMELOCK..offset::BUMP].try_into().unwrap()),
    })
}

/// Empties the escrow: `amount` to `paid`, whatever is left -- the rent deposit
/// -- to `rest`. Draining an account to zero lamports is how Solana deletes it,
/// so this is also the close. The data is zeroed first so that a same-slot
/// resurrection cannot find a live-looking escrow.
fn close_to(
    escrow: &AccountInfo,
    amount: u64,
    paid: &AccountInfo,
    rest: &AccountInfo,
) -> ProgramResult {
    let total = escrow.lamports();
    if total < amount {
        // Unreachable with an escrow this program created; a cheap assertion
        // against a future change that lets lamports leave by another route.
        return Err(ProgramError::InsufficientFunds);
    }
    let remainder = total - amount;

    escrow.try_borrow_mut_data()?.fill(0);
    **escrow.try_borrow_mut_lamports()? = 0;

    // `paid` and `rest` are the same account on the refund path, so the credits
    // are applied one at a time rather than computed against a stale read.
    **paid.try_borrow_mut_lamports()? = paid
        .lamports()
        .checked_add(amount)
        .ok_or(ProgramError::ArithmeticOverflow)?;
    **rest.try_borrow_mut_lamports()? = rest
        .lamports()
        .checked_add(remainder)
        .ok_or(ProgramError::ArithmeticOverflow)?;
    Ok(())
}
