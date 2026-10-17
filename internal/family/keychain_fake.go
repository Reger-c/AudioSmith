package family

import (
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"sync"

	"github.com/hehehai/actnowkit/credentials"
)

// FakeKeychain stands in for /usr/bin/security in tests
// (credentials.Keychain.Run): generic passwords in memory, and a record
// of every item read, so that tests can tell that no shared secret was
// read before the user agreed. Tests never touch the real Keychain.
type FakeKeychain struct {
	mu    sync.Mutex
	items map[string]string // service + "/" + account → secret
	reads []string          // service + "/" + account, in order
}

// NewFakeKeychain is an empty fake.
func NewFakeKeychain() *FakeKeychain { return &FakeKeychain{items: map[string]string{}} }

// Keychain is a credentials.Keychain over the fake.
func (f *FakeKeychain) Keychain() credentials.Keychain { return credentials.Keychain{Run: f.Run} }

// Put stores an item as an app would have.
func (f *FakeKeychain) Put(service, account, secret string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items[service+"/"+account] = secret
}

// Item returns an item; ok false when there is none.
func (f *FakeKeychain) Item(service, account string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.items[service+"/"+account]
	return v, ok
}

// Read reports whether the item was read since the last Forget.
func (f *FakeKeychain) Read(service, account string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.reads, service+"/"+account)
}

// Forget clears the record of reads.
func (f *FakeKeychain) Forget() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads = nil
}

// Run imitates security's add-, find- and delete-generic-password, as
// credentials.Keychain calls them.
func (f *FakeKeychain) Run(stdin string, args ...string) ([]byte, []byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(args) == 1 && args[0] == "-i" {
		args = splitWords(strings.TrimSpace(stdin))
	}
	if len(args) == 0 {
		return nil, nil, errors.New("fake security: no command")
	}
	opt := map[string]string{}
	for i := 1; i+1 < len(args); i++ {
		if strings.HasPrefix(args[i], "-") && args[i] != "-U" && args[i] != "-g" {
			opt[args[i]] = args[i+1]
			i++
		}
	}
	key := opt["-s"] + "/" + opt["-a"]
	switch args[0] {
	case "add-generic-password":
		b, err := hex.DecodeString(opt["-X"])
		if err != nil {
			return nil, nil, err
		}
		f.items[key] = string(b)
		return nil, nil, nil
	case "find-generic-password":
		f.reads = append(f.reads, key)
		v, ok := f.items[key]
		if !ok {
			return nil, nil, credentials.ErrNotFound
		}
		return nil, []byte("password: 0x" + strings.ToUpper(hex.EncodeToString([]byte(v))) + "  \"…\"\n"), nil
	case "delete-generic-password":
		if _, ok := f.items[key]; !ok {
			return nil, nil, credentials.ErrNotFound
		}
		delete(f.items, key)
		return nil, nil, nil
	}
	return nil, nil, errors.New("fake security: unknown command " + args[0])
}

// splitWords splits security's interactive line, honouring double quotes.
func splitWords(s string) []string {
	var out []string
	var cur strings.Builder
	quoted, has := false, false
	for _, r := range s {
		switch {
		case r == '"':
			quoted, has = !quoted, true
		case r == ' ' && !quoted:
			if has {
				out = append(out, cur.String())
				cur.Reset()
				has = false
			}
		default:
			cur.WriteRune(r)
			has = true
		}
	}
	if has {
		out = append(out, cur.String())
	}
	return out
}
