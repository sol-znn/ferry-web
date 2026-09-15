# Security Hardening Review: Ferry

## Evidence Basis

This proposal derives from the source audit of Ferry at `be879e1bd61a9922ee58e16d32095bafa43bdf6e`. I traced the protocol and UI paths behind seven reportable findings. The canonical scan was unsealed when this derived analysis was written. Most validation was static; only the isolated inbox date expression was executed. The repository was not modified.

## Constraints

We assume a balanced repair plan that preserves a static client, external wallet custody, both initiating-chain orderings and offline recovery. No measured performance budget was supplied. Configured chain nodes remain trusted for observations.

## Opportunity Portfolio

| Opportunity | Evidence | Options | Recommendation | Proposal |
| --- | --- | --- | --- | --- |
| Own the swap commitment boundary | Underpayment, unconfirmed funding, incomplete Zenon terms, noncanonical scripts and mutable funded contracts | 1. Strengthen existing methods; 2. Freeze terms and issue validated action plans | Ship immediate guards, then centralize action authority | [Commitment boundary](proposals/commitment-boundary.md) |

The CLI shell-injection and inbox-rendering findings warrant independent local fixes. They do not justify enlarging the protocol redesign.

## Recommendation Summary

I recommend Option 2 as the target because the same incomplete-state pattern reaches several irreversible operations. We can introduce it without adding a server or taking wallet custody. Option 1 is the practical immediate protection and remains a reasonable stopping point if the action surface stays small and entrypoint tests prevent drift. The important cost in Option 2 is migrating existing funded records safely; performance effects are unmeasured.

## Next Decisions

We need to choose whether to stop at the guarded methods or proceed to validated plans, set a confirmation and time-margin policy, and resolve supported-wallet publication behavior for expired Unlocks. The [proposal](proposals/commitment-boundary.md) contains the evidence map, alternatives, rollout and verification work. No proposed change is implemented.

