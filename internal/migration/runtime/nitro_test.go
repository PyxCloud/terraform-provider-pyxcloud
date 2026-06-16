package runtime

// nitro_test.go is the known-answer / verification-path test for the REAL Nitro
// attestation backend. CI has no /dev/nsm device, so we cannot call the genuine
// NSM. Instead we build a SYNTHETIC-but-structurally-genuine Nitro attestation
// document — a COSE_Sign1(ES384) over a CBOR payload carrying PCRs + the enclave
// public_key + the nonce, signed by a leaf certificate that chains to a synthetic
// root — and assert that:
//
//	(a) ParseNitroDocument + VerifyNitroDocument decode it, verify the COSE_Sign1
//	    signature + cert chain, and recover the PCR0 measurement, nonce, and pubkey;
//	(b) nonce binding rejects a replayed challenge;
//	(c) public-key binding rejects a wrong run key;
//	(d) a forged measurement (re-signed with a different root the verifier doesn't
//	    trust) FAILS verification → no accepted measurement, no key release.
//
// This is the exact verification path pyx-backend's AttestationVerifier runs, so
// the test doubles as the provider↔backend evidence-format contract.

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	cose "github.com/veraison/go-cose"
)

// nitroTestVector holds a synthetic Nitro PKI + fixed inputs for known-answer tests.
type nitroTestVector struct {
	roots     *x509.CertPool
	rootCert  *x509.Certificate
	leafCert  *x509.Certificate
	leafKey   *ecdsa.PrivateKey
	publicKey []byte // the enclave public_key bound into the document
	pcr0      []byte // the PCR0 measurement
	now       time.Time
}

// newNitroTestVector mints a synthetic P-384 root + leaf and fixed measurement/key,
// modeling the AWS Nitro attestation PKI (ECDSA P-384 / ES384) without a real NSM.
func newNitroTestVector(t *testing.T) *nitroTestVector {
	t.Helper()
	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)

	rootKey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatalf("root key: %v", err)
	}
	rootTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "synthetic-nitro-root"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTmpl, rootTmpl, &rootKey.PublicKey, rootKey)
	if err != nil {
		t.Fatalf("root cert: %v", err)
	}
	rootCert, _ := x509.ParseCertificate(rootDER)

	leafKey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatalf("leaf key: %v", err)
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "synthetic-nitro-leaf"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, rootCert, &leafKey.PublicKey, rootKey)
	if err != nil {
		t.Fatalf("leaf cert: %v", err)
	}
	leafCert, _ := x509.ParseCertificate(leafDER)

	roots := x509.NewCertPool()
	roots.AddCert(rootCert)

	// Fixed known-answer inputs: a 48-byte (SHA384-sized) PCR0 and a 32-byte pubkey.
	pcr0 := bytes.Repeat([]byte{0xAB}, 48)
	pub := bytes.Repeat([]byte{0xCD}, 32)

	return &nitroTestVector{
		roots:     roots,
		rootCert:  rootCert,
		leafCert:  leafCert,
		leafKey:   leafKey,
		publicKey: pub,
		pcr0:      pcr0,
		now:       now,
	}
}

// signDocument builds a real COSE_Sign1(ES384) over the CBOR attestation payload,
// binding the given nonce + pubKey, signed by the leaf key (chaining to the root).
func (v *nitroTestVector) signDocument(t *testing.T, nonce, pubKey []byte) []byte {
	t.Helper()
	payload := NitroDocument{
		ModuleID:    "i-synthetic-enc",
		Timestamp:   uint64(v.now.UnixMilli()),
		Digest:      "SHA384",
		PCRs:        map[uint][]byte{0: v.pcr0, 1: bytes.Repeat([]byte{0x01}, 48)},
		Certificate: v.leafCert.Raw,
		CABundle:    [][]byte{v.rootCert.Raw},
		PublicKey:   pubKey,
		Nonce:       nonce,
	}
	payloadCBOR, err := cbor.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	signer, err := cose.NewSigner(cose.AlgorithmES384, v.leafKey)
	if err != nil {
		t.Fatalf("cose signer: %v", err)
	}
	msg := cose.NewSign1Message()
	msg.Payload = payloadCBOR
	msg.Headers.Protected.SetAlgorithm(cose.AlgorithmES384)
	if err := msg.Sign(rand.Reader, nil, signer); err != nil {
		t.Fatalf("cose sign: %v", err)
	}
	doc, err := msg.MarshalCBOR()
	if err != nil {
		t.Fatalf("marshal COSE_Sign1: %v", err)
	}
	return doc
}

// TestNitroKnownAnswerVerify is the happy-path KAT: a genuine COSE_Sign1/CBOR
// document verifies, the chain checks to the trusted root, and the bound nonce +
// public key + PCR0 are recovered exactly.
func TestNitroKnownAnswerVerify(t *testing.T) {
	t.Parallel()
	v := newNitroTestVector(t)
	nonce := bytes.Repeat([]byte{0x42}, 32)
	doc := v.signDocument(t, nonce, v.publicKey)

	got, err := VerifyNitroDocument(doc, NitroVerifyOptions{
		Roots:             v.roots,
		ExpectedNonce:     nonce,
		ExpectedPublicKey: v.publicKey,
		Now:               v.now,
	})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !bytes.Equal(got.Measurement(), v.pcr0) {
		t.Errorf("PCR0 measurement mismatch")
	}
	if !bytes.Equal(got.Nonce, nonce) {
		t.Errorf("nonce mismatch")
	}
	if !bytes.Equal(got.PublicKey, v.publicKey) {
		t.Errorf("public key mismatch")
	}
}

// TestNitroParseExtractsFields proves ParseNitroDocument lifts the structural
// fields without needing the trust root (used by both BE and provider).
func TestNitroParseExtractsFields(t *testing.T) {
	t.Parallel()
	v := newNitroTestVector(t)
	nonce := bytes.Repeat([]byte{0x07}, 32)
	doc := v.signDocument(t, nonce, v.publicKey)
	parsed, err := ParseNitroDocument(doc)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !bytes.Equal(parsed.Measurement(), v.pcr0) || !bytes.Equal(parsed.Nonce, nonce) {
		t.Error("parsed fields do not match")
	}
}

// TestNitroReplayedNonceRejected proves nonce binding: a document carrying a stale
// nonce is rejected (anti-replay, §3 step 2).
func TestNitroReplayedNonceRejected(t *testing.T) {
	t.Parallel()
	v := newNitroTestVector(t)
	doc := v.signDocument(t, bytes.Repeat([]byte{0x42}, 32), v.publicKey)
	_, err := VerifyNitroDocument(doc, NitroVerifyOptions{
		Roots:         v.roots,
		ExpectedNonce: bytes.Repeat([]byte{0x99}, 32), // different challenge
		Now:           v.now,
	})
	if err == nil {
		t.Fatal("expected replay rejection on mismatched nonce")
	}
}

// TestNitroWrongPublicKeyRejected proves key binding: a document bound to a
// different public key than this run's is rejected.
func TestNitroWrongPublicKeyRejected(t *testing.T) {
	t.Parallel()
	v := newNitroTestVector(t)
	nonce := bytes.Repeat([]byte{0x42}, 32)
	doc := v.signDocument(t, nonce, bytes.Repeat([]byte{0x11}, 32)) // bound to other key
	_, err := VerifyNitroDocument(doc, NitroVerifyOptions{
		Roots:             v.roots,
		ExpectedNonce:     nonce,
		ExpectedPublicKey: v.publicKey, // this run's key
		Now:               v.now,
	})
	if err == nil {
		t.Fatal("expected rejection when public_key is not bound to this run")
	}
}

// TestNitroForgedSignatureRejected proves a document signed by a leaf that chains
// to an UNTRUSTED root (a forged attestation) fails verification → no accepted
// measurement, no key release. This is the runtime analogue of the opacity
// "forged measurement" guarantee at the attestation layer.
func TestNitroForgedSignatureRejected(t *testing.T) {
	t.Parallel()
	v := newNitroTestVector(t)      // the verifier trusts v.roots
	forger := newNitroTestVector(t) // a different, untrusted PKI
	nonce := bytes.Repeat([]byte{0x42}, 32)
	doc := forger.signDocument(t, nonce, forger.publicKey)

	_, err := VerifyNitroDocument(doc, NitroVerifyOptions{
		Roots: v.roots, // does NOT include the forger's root
		Now:   v.now,
	})
	if err == nil {
		t.Fatal("expected verification failure for a document not chaining to a trusted root")
	}
}

// TestNitroTamperedPayloadRejected proves a bit-flip in the signed document breaks
// the COSE_Sign1 signature → rejected.
func TestNitroTamperedPayloadRejected(t *testing.T) {
	t.Parallel()
	v := newNitroTestVector(t)
	nonce := bytes.Repeat([]byte{0x42}, 32)
	doc := v.signDocument(t, nonce, v.publicKey)
	tampered := append([]byte{}, doc...)
	tampered[len(tampered)/2] ^= 0xFF
	if _, err := VerifyNitroDocument(tampered, NitroVerifyOptions{Roots: v.roots, Now: v.now}); err == nil {
		t.Fatal("expected verification failure on a tampered document")
	}
}

// TestNitroNoRootRejected proves verification refuses when no trust root is given.
func TestNitroNoRootRejected(t *testing.T) {
	t.Parallel()
	v := newNitroTestVector(t)
	doc := v.signDocument(t, bytes.Repeat([]byte{0x42}, 32), v.publicKey)
	if _, err := VerifyNitroDocument(doc, NitroVerifyOptions{Now: v.now}); err == nil {
		t.Fatal("expected failure with no trusted root configured")
	}
}
