package main

import (
	"strings"
	"testing"
)

// The linker flag is a string, and a string set by hand is a string that can be
// mistyped. "development", "DEV", "1" and an empty value all have to land
// somewhere, and the direction they land in is a safety property rather than a
// preference: a build that is really prod but reports dev would file mainnet
// swaps under the development namespace, where the production build cannot
// find them.
func TestOnlyTheExactStringSelectsTheDevInstance(t *testing.T) {
	original := BuildEnv
	t.Cleanup(func() { BuildEnv = original })

	for _, value := range []string{"dev"} {
		BuildEnv = value
		if !IsDev() {
			t.Errorf("BuildEnv=%q: want the dev instance", value)
		}
	}
	for _, value := range []string{"", "prod", "development", "DEV", "Dev", "1", "true", "staging"} {
		BuildEnv = value
		if IsDev() {
			t.Errorf("BuildEnv=%q: want prod, got the dev instance", value)
		}
		if Env() != EnvProd {
			t.Errorf("BuildEnv=%q: Env() = %q, want %q", value, Env(), EnvProd)
		}
	}
}

// The default matters on its own: a build that passes no linker flag at all is
// the one a contributor produces with a bare `go build`, and it must not be a
// development build.
func TestTheDefaultInstanceIsProd(t *testing.T) {
	if BuildEnv != EnvProd {
		t.Fatalf("BuildEnv defaults to %q, want %q", BuildEnv, EnvProd)
	}
}

// Two instances deployed to one origin share a localStorage namespace, and the
// whole reason the prefix is a function is so that they do not share the swaps
// inside it. The prefixes must differ, and neither may be a prefix of the other
// — List walks every key on the origin and keeps the ones that start with this
// string, so "ferry.swap." matching "ferry.swap.dev.<id>" would put development
// swaps in the production list.
func TestTheTwoInstancesDoNotShareAStorageNamespace(t *testing.T) {
	original := BuildEnv
	t.Cleanup(func() { BuildEnv = original })

	BuildEnv = EnvDev
	dev := StorageKeyPrefix()
	BuildEnv = EnvProd
	prod := StorageKeyPrefix()

	if dev == prod {
		t.Fatalf("both instances store under %q", dev)
	}
	if strings.HasPrefix(dev, prod) || strings.HasPrefix(prod, dev) {
		t.Fatalf("one namespace is a prefix of the other: dev=%q prod=%q; List would read both", dev, prod)
	}
}

// A swap written by one instance must be invisible to the other, which is the
// behaviour the prefixes exist to produce rather than merely imply.
func TestASwapWrittenByOneInstanceIsNotListedByTheOther(t *testing.T) {
	original := BuildEnv
	t.Cleanup(func() { BuildEnv = original })

	// One origin: the same backing storage under both builds, which is exactly
	// what deploying them to one host would give.
	backing := NewMemStorage()

	BuildEnv = EnvDev
	devStore := NewStore(backing)
	key, err := NewSwapKey()
	if err != nil {
		t.Fatalf("NewSwapKey: %v", err)
	}
	_, hash, err := NewSecret()
	if err != nil {
		t.Fatalf("NewSecret: %v", err)
	}
	sw := &Swap{
		ID: "00112233445566aa", Network: "regtest", Role: RoleInitiator,
		Leg: LegSend, State: StateDraft, Key: key, SecretHash: hash, AmountSats: 400_000,
	}
	if err := devStore.Save(sw); err != nil {
		t.Fatalf("Save: %v", err)
	}

	BuildEnv = EnvProd
	prodStore := NewStore(backing)
	swaps, err := prodStore.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(swaps) != 0 {
		t.Fatalf("the production build lists %d development swap(s); want none", len(swaps))
	}
	if _, err := prodStore.Load(sw.ID); err == nil {
		t.Fatal("the production build loaded a development swap by id")
	}

	// And the dev build still sees its own, so this is separation rather than
	// the record having failed to be written at all.
	BuildEnv = EnvDev
	if swaps, err := devStore.List(); err != nil || len(swaps) != 1 {
		t.Fatalf("the development build lists %d of its own swaps (err %v); want 1", len(swaps), err)
	}
}
