//go:build !js

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// The same call table, driven from a terminal.
//
// It exists so the engine can be exercised end to end against two live chains
// without a browser in the way. That matters more than it sounds: a swap has
// two participants, and two participants in a browser means two profiles and a
// person clicking in each. Here it is two directories.
//
// It is also what keeps every file above this one compilable on a normal GOOS,
// so `go test` and `go vet` see the whole module rather than only the parts
// that do not touch syscall/js.
//
//	solzen -store DIR '{"method":"swap.list"}'
//	echo '{"method":"env"}' | solzen -store DIR
func main() {
	dir := ""
	args := os.Args[1:]
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "-store":
			if len(args) < 2 {
				die("-store needs a directory")
			}
			dir, args = args[1], args[2:]
		case "-h", "-help", "--help":
			usage()
			return
		default:
			die(fmt.Sprintf("unknown flag %q", args[0]))
		}
	}
	if dir == "" {
		dir = os.Getenv("SOLZEN_STORE")
	}
	if dir == "" {
		die("a store directory is required: -store DIR, or SOLZEN_STORE")
	}

	request := strings.Join(args, " ")
	if strings.TrimSpace(request) == "" {
		raw, err := io.ReadAll(os.Stdin)
		if err != nil {
			die(err.Error())
		}
		request = string(raw)
	}
	if strings.TrimSpace(request) == "" {
		usage()
		os.Exit(2)
	}

	backend, err := newFileBackend(dir)
	if err != nil {
		die(err.Error())
	}
	store := NewStore(backend)
	manager := NewManager(store)

	cfg := store.LoadConfig()
	if cfg.SolanaURL == "" && cfg.ZenonURL == "" && cfg.SolProgram == "" {
		cfg = DefaultConfig()
	}
	if cfg.SolProgram == "" {
		cfg.SolProgram = SolProgramID
	}
	if v := os.Getenv("SOLZEN_SOLANA_URL"); v != "" {
		cfg.SolanaURL = v
	}
	if v := os.Getenv("SOLZEN_ZENON_URL"); v != "" {
		cfg.ZenonURL = v
	}
	if v := os.Getenv("SOLZEN_PROGRAM"); v != "" {
		cfg.SolProgram = v
	}
	manager.SetConfig(cfg)

	// Proof of work goes to stderr so that stdout stays a single JSON document
	// a caller can pipe into jq.
	var last time.Time
	progress := func(hashes uint64) {
		if time.Since(last) < time.Second {
			return
		}
		last = time.Now()
		fmt.Fprintf(os.Stderr, "\rmining plasma: %.1fM hashes", float64(hashes)/1e6)
	}

	resp := NewAPI(manager).Handle(context.Background(), request, progress)
	if last != (time.Time{}) {
		fmt.Fprintln(os.Stderr)
	}
	out, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		die(err.Error())
	}
	fmt.Println(string(out))
	if resp.Error != "" {
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `solzen -- the swap engine, driven from a terminal

  solzen -store DIR '<json request>'
  echo '<json request>' | solzen -store DIR

Requests are the same ones the page makes; see wasm/api.go for the table.
Environment: SOLZEN_STORE, SOLZEN_SOLANA_URL, SOLZEN_ZENON_URL, SOLZEN_PROGRAM.
`)
}

func die(msg string) {
	fmt.Fprintln(os.Stderr, "solzen:", msg)
	os.Exit(2)
}

// fileBackend is one file per key, which makes a store readable with `cat` and
// a swap record something a person can look at while debugging.
type fileBackend struct {
	mu  sync.Mutex
	dir string
}

func newFileBackend(dir string) (*fileBackend, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating the store directory: %w", err)
	}
	return &fileBackend{dir: dir}, nil
}

// Keys are turned into filenames by replacing the separator, so the
// storeKeyPrefix namespacing survives into the directory listing.
func (b *fileBackend) path(key string) string {
	return filepath.Join(b.dir, strings.ReplaceAll(key, "/", "_")+".json")
}

func (b *fileBackend) Get(key string) (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	raw, err := os.ReadFile(b.path(key))
	if err != nil {
		return "", false
	}
	return string(raw), true
}

func (b *fileBackend) Set(key, value string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return os.WriteFile(b.path(key), []byte(value), 0o600)
}

func (b *fileBackend) Delete(key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	err := os.Remove(b.path(key))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (b *fileBackend) Keys() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	entries, err := os.ReadDir(b.dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		out = append(out, strings.TrimSuffix(name, ".json"))
	}
	sort.Strings(out)
	return out
}
