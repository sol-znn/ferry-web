//go:build !fastclock

package main

// The two creation-time minimums, at the values that ship.
//
// They live in their own file, behind a build tag, because the end-to-end
// timeout test cannot run against them: a swap whose legs are four hours apart
// takes four hours to time out. `-tags fastclock` selects limits_fastclock.go
// instead, and the test asserts through the `env` call that it got the build it
// asked for.
//
// A build tag rather than a Config field, deliberately. These are the
// participant's protection, and the only thing a user-facing knob for them
// could do is weaken it. A compiler flag is visible in the build command,
// cannot be reached from the page, and leaves the file that ships as the file
// under test.

// MinLegGapSeconds is the smallest acceptable margin between the participant's
// leg expiring and the initiator's.
//
// It is the participant's entire protection. When the participant's leg
// expires, the initiator can no longer claim it -- but if the initiator claimed
// it a moment before, the participant now needs time to notice the revealed
// secret and use it. Accepting a swap whose gap is too small is accepting that
// a slow block, or a night's sleep, costs them the money.
const MinLegGapSeconds int64 = 4 * 3600

// MinRemainingSeconds is how much of a leg must be left for it to be worth
// acting on. An HTLC handed over minutes before expiry is not an offer, it is a
// race the person who timed it expects to win.
const MinRemainingSeconds int64 = 30 * 60
