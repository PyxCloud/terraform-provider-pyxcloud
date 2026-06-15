package migration

// seal.go implements the small, self-contained HPKE-style sealing primitive the
// migration engine relies on. It is an RFC 9180-shaped construction:
//
//	DHKEM(X25519, HKDF-SHA256) + HKDF-SHA256 + ChaCha20-Poly1305 (AEAD)
//
// built from golang.org/x/crypto/{curve25519,hkdf,chacha20poly1305}, because the
// vendored x/crypto does not ship a public hpke package. This is deliberately the
// ONLY cryptography in the provider-side tree, and it carries ZERO migration
// logic: it seals/opens opaque bytes. What those bytes mean (the step program) is
// the backend's industrial secret — this file never interprets them.
//
// Trust-boundary role (MIGRATION.md §3):
//   - The provider generates a per-run ephemeral X25519 keypair. The PUBLIC key
//     crosses to the backend in the plan request; the PRIVATE key never leaves
//     process memory and is zeroized at run end.
//   - The backend seals the opaque execution bundle (and the scoped cloud creds)
//     TO that ephemeral public key. Only a holder of the ephemeral private key can
//     open it — and in the real system that private key is released only inside an
//     attested enclave. Here the sealed-WASM runtime models that boundary: it opens
//     the bundle inside the sandbox and never returns plaintext to the caller.
//
// Opacity is structural: Open returns plaintext only to its direct caller (the
// runtime, inside the seal), and the provider/runner code paths never call Open —
// they only ever hold Sealed ciphertext.
import (
	"crypto/rand"
	"fmt"
	"io"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"

	"crypto/sha256"
)

// sealInfo binds derived keys to this construction + purpose so a ciphertext for
// one purpose can never be opened as another (domain separation).
const sealInfo = "pyxcloud/migration/seal/v1 X25519-HKDF-SHA256 ChaCha20Poly1305"

// Sealed is an opaque sealed payload: an ephemeral KEM public key plus the AEAD
// ciphertext. It is treated as ciphertext bytes everywhere on the provider/runner
// side — never parsed for meaning.
type Sealed struct {
	// KEMPub is the sender's ephemeral X25519 public key (the encapsulation).
	KEMPub [32]byte `json:"kem_pub"`
	// Ciphertext is nonce-prefixed ChaCha20-Poly1305 output over the opaque payload.
	Ciphertext []byte `json:"ciphertext"`
}

// sealTo encapsulates to recipientPub and AEAD-encrypts plaintext. The backend
// uses this shape to seal the bundle to the provider's ephemeral public key; the
// tests use it to forge bundles and prove the runtime only opens what it can.
//
// aad is additional authenticated data — the runtime binds the attestation
// measurement here so a ciphertext sealed for one measurement cannot be opened
// under a different (forged) one.
func sealTo(recipientPub [32]byte, plaintext, aad []byte) (Sealed, error) {
	// Ephemeral sender key (the KEM encapsulation key).
	var ephPriv [32]byte
	if _, err := io.ReadFull(rand.Reader, ephPriv[:]); err != nil {
		return Sealed{}, fmt.Errorf("seal: ephemeral key: %w", err)
	}
	defer zeroize(ephPriv[:])

	ephPubSlice, err := curve25519.X25519(ephPriv[:], curve25519.Basepoint)
	if err != nil {
		return Sealed{}, fmt.Errorf("seal: derive ephemeral public: %w", err)
	}
	shared, err := curve25519.X25519(ephPriv[:], recipientPub[:])
	if err != nil {
		return Sealed{}, fmt.Errorf("seal: ecdh: %w", err)
	}
	defer zeroize(shared)

	var ephPub [32]byte
	copy(ephPub[:], ephPubSlice)

	key, err := deriveKey(shared, ephPub, recipientPub)
	if err != nil {
		return Sealed{}, err
	}
	defer zeroize(key)

	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return Sealed{}, fmt.Errorf("seal: aead: %w", err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return Sealed{}, fmt.Errorf("seal: nonce: %w", err)
	}
	ct := aead.Seal(nonce, nonce, plaintext, aad)
	return Sealed{KEMPub: ephPub, Ciphertext: ct}, nil
}

// open decapsulates with recipientPriv and AEAD-decrypts. It is called ONLY from
// inside a runtime's sealed boundary; the returned plaintext must never escape it.
// A wrong recipient key, a tampered ciphertext, or a mismatched aad (e.g. a forged
// attestation measurement) all fail authentication and return an error — the key
// is never "released" and no plaintext is produced.
func open(recipientPriv [32]byte, s Sealed, aad []byte) ([]byte, error) {
	var recipientPub [32]byte
	pub, err := curve25519.X25519(recipientPriv[:], curve25519.Basepoint)
	if err != nil {
		return nil, fmt.Errorf("open: derive recipient public: %w", err)
	}
	copy(recipientPub[:], pub)

	shared, err := curve25519.X25519(recipientPriv[:], s.KEMPub[:])
	if err != nil {
		return nil, fmt.Errorf("open: ecdh: %w", err)
	}
	defer zeroize(shared)

	key, err := deriveKey(shared, s.KEMPub, recipientPub)
	if err != nil {
		return nil, err
	}
	defer zeroize(key)

	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, fmt.Errorf("open: aead: %w", err)
	}
	if len(s.Ciphertext) < aead.NonceSize() {
		return nil, fmt.Errorf("open: ciphertext too short")
	}
	nonce, ct := s.Ciphertext[:aead.NonceSize()], s.Ciphertext[aead.NonceSize():]
	pt, err := aead.Open(nil, nonce, ct, aad)
	if err != nil {
		// Authentication failure: forged/mismatched measurement, wrong key, or
		// tampered ciphertext. No plaintext, no key release.
		return nil, fmt.Errorf("open: authentication failed (no key release): %w", err)
	}
	return pt, nil
}

// deriveKey runs HKDF-SHA256 over the ECDH shared secret, salted with the KEM
// context (both public keys) and domain-separated by sealInfo, to a 32-byte
// ChaCha20-Poly1305 key.
func deriveKey(shared []byte, ephPub, recipientPub [32]byte) ([]byte, error) {
	salt := make([]byte, 0, 64)
	salt = append(salt, ephPub[:]...)
	salt = append(salt, recipientPub[:]...)
	r := hkdf.New(sha256.New, shared, salt, []byte(sealInfo))
	key := make([]byte, chacha20poly1305.KeySize)
	if _, err := io.ReadFull(r, key); err != nil {
		return nil, fmt.Errorf("seal: hkdf: %w", err)
	}
	return key, nil
}
