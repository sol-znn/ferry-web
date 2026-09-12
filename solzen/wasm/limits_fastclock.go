//go:build fastclock

package main

// The same two minimums, small enough for a test to sit through.
//
// Built only with `-tags fastclock`, which nothing that ships passes. Both
// values gate whether a swap may be *created*: neither is consulted by a
// refund, by the expiry arithmetic, or by any on-chain call, so every path the
// timeout test exercises after creation is the shipped path. See limits.go.
//
// They are not zero, because a test that switches the ordering rule off stops
// testing it. Two minutes of gap is still arithmetic; it is just arithmetic
// that finishes.

const MinLegGapSeconds int64 = 120

const MinRemainingSeconds int64 = 60
