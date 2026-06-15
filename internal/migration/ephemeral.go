package migration

// ephemeral.go owns the per-run ephemeral X25519 keypair (MIGRATION.md §3.1):
// forward secrecy with no replay. The private key lives ONLY in process memory
// and is zeroized at run end; only the public key crosses the trust boundary in
// the plan request.

import (
	"crypto/rand"
	"fmt"
	"io"

	"golang.org/x/crypto/curve25519"
)

// ephemeralKey is a per-migration-run X25519 keypair. Private stays in memory and
// must be Zeroize()d when the run completes (success, failure, or rollback).
type ephemeralKey struct {
	priv    [32]byte
	pub     [32]byte
	cleared bool
}

// newEphemeralKey generates a fresh per-run X25519 keypair from crypto/rand.
func newEphemeralKey() (*ephemeralKey, error) {
	k := &ephemeralKey{}
	if _, err := io.ReadFull(rand.Reader, k.priv[:]); err != nil {
		return nil, fmt.Errorf("ephemeral key: read random: %w", err)
	}
	pub, err := curve25519.X25519(k.priv[:], curve25519.Basepoint)
	if err != nil {
		return nil, fmt.Errorf("ephemeral key: derive public: %w", err)
	}
	copy(k.pub[:], pub)
	return k, nil
}

// PublicKey returns the public half — the ONLY part that crosses the boundary.
func (k *ephemeralKey) PublicKey() [32]byte { return k.pub }

// private returns the private key for use inside a sealed runtime boundary only.
// It is package-private and never exposed to the provider/runner code paths.
func (k *ephemeralKey) private() [32]byte { return k.priv }

// Zeroize wipes the private key from memory. Idempotent. After Zeroize the key
// can no longer open anything — a captured bundle becomes useless (§3.5).
func (k *ephemeralKey) Zeroize() {
	if k == nil || k.cleared {
		return
	}
	zeroize(k.priv[:])
	k.cleared = true
}

// cleared reports whether the private key has been wiped (used by opacity tests).
func (k *ephemeralKey) isCleared() bool { return k == nil || k.cleared }

// zeroize overwrites a byte slice with zeros. Best-effort in-memory wipe; Go's GC
// can copy memory, but this removes the obvious live copy on the deterministic
// teardown path the engine controls.
func zeroize(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
