package runtime

// nitro.go is the REAL AWS Nitro Enclaves attestation backend (MIGRATION.md §3),
// replacing the phase-1 STUB for the confidential-container Nitro path. It splits
// cleanly into two halves so CI (which has no NSM device) can exercise the
// verification path with a known-answer vector:
//
//   - GENERATION (genuine, enclave-only): nitro_nsm_linux.go obtains a real
//     COSE_Sign1-wrapped, CBOR-encoded attestation document from the Nitro Security
//     Module (NSM) via the /dev/nsm ioctl, binding the backend's challenge `nonce`
//     and the run's `ephemeralPubKey` into the document (request.Attestation's
//     Nonce + PublicKey fields). It is gated behind capability detection: outside a
//     Nitro enclave the call DEGRADES CLEANLY with a documented error — never a
//     silent fake.
//
//   - VERIFICATION (portable, this file): ParseNitroDocument + VerifyNitroDocument
//     decode the COSE_Sign1/CBOR document, extract the PCRs (the measurement the
//     backend pins), the embedded public_key and nonce (so the BE's
//     AttestationVerifier can bind the released key to THIS enclave + THIS run and
//     reject replays), and verify the COSE_Sign1 signature against the leaf
//     certificate + its chain to a trusted root. This mirrors exactly what
//     pyx-backend's AttestationVerifier consumes (substrate, format, measurement,
//     nonce, document).
//
// This file carries ZERO migration logic. It only parses/verifies attestation
// evidence and never sees plaintext bundle/creds.

import (
	"crypto/ecdsa"
	"crypto/x509"
	"errors"
	"fmt"
	"time"

	"github.com/fxamacker/cbor/v2"
	cose "github.com/veraison/go-cose"
)

// nitroNSMPath is the Nitro Security Module device. Its presence is the capability
// gate for the REAL attestation-document generation path.
const nitroNSMPath = "/dev/nsm"

// NitroFormat is the evidence Format tag for a genuine Nitro attestation document.
// The backend verifier dispatches on it.
const NitroFormat = "nitro-attestation-doc"

// nitroMeasurementPCR is the PCR index whose value the backend pins as the
// enclave-image measurement. PCR0 is the hash of the enclave image file (EIF) —
// the canonical "what code is running" measurement for Nitro (PCR1=kernel/bootstrap,
// PCR2=application). We surface PCR0 as Evidence.Measurement.
const nitroMeasurementPCR uint = 0

// NitroDocument is the decoded payload of a Nitro attestation document (the CBOR
// map inside the COSE_Sign1 payload). Field tags match the AWS NSM document schema.
// It is the verification-side view of what an enclave attested to.
type NitroDocument struct {
	ModuleID    string          `cbor:"module_id"`
	Timestamp   uint64          `cbor:"timestamp"`
	Digest      string          `cbor:"digest"`
	PCRs        map[uint][]byte `cbor:"pcrs"`
	Certificate []byte          `cbor:"certificate"` // leaf cert (signs the COSE_Sign1)
	CABundle    [][]byte        `cbor:"cabundle"`    // intermediate chain, root-first
	PublicKey   []byte          `cbor:"public_key"`  // the enclave's bound public key
	UserData    []byte          `cbor:"user_data"`
	Nonce       []byte          `cbor:"nonce"` // the backend's freshness challenge
}

// Measurement returns the PCR0 value (the enclave-image measurement). It is what
// the backend pins as the expected signed value and what the bundle is sealed to
// (AAD). Returns nil if PCR0 is absent.
func (d *NitroDocument) Measurement() []byte {
	if d == nil || d.PCRs == nil {
		return nil
	}
	return d.PCRs[nitroMeasurementPCR]
}

// ParseNitroDocument decodes a COSE_Sign1-wrapped, CBOR-encoded Nitro attestation
// document into its payload WITHOUT verifying the signature. Use VerifyNitroDocument
// for the trust decision; this is the structural decode the backend verifier and the
// known-answer test share.
func ParseNitroDocument(coseSign1 []byte) (*NitroDocument, error) {
	var msg cose.Sign1Message
	if err := msg.UnmarshalCBOR(coseSign1); err != nil {
		return nil, fmt.Errorf("nitro: not a COSE_Sign1 document: %w", err)
	}
	var doc NitroDocument
	if err := cbor.Unmarshal(msg.Payload, &doc); err != nil {
		return nil, fmt.Errorf("nitro: attestation payload not CBOR: %w", err)
	}
	if doc.ModuleID == "" || doc.Digest == "" || doc.Timestamp == 0 || len(doc.PCRs) == 0 || len(doc.Certificate) == 0 {
		return nil, errors.New("nitro: attestation document missing required fields")
	}
	if doc.Digest != "SHA384" {
		return nil, fmt.Errorf("nitro: unexpected PCR digest %q (want SHA384)", doc.Digest)
	}
	return &doc, nil
}

// NitroVerifyOptions configures attestation verification. Roots is the set of
// trusted Nitro attestation roots (in production: the pinned AWS Nitro Enclaves
// root CA; in the known-answer test: a synthetic root). ExpectedNonce and
// ExpectedPublicKey, when non-nil, are checked against the document so the verifier
// binds the released key to THIS run and rejects replays (§3 steps 2–4). Now is
// injectable so the KAT can verify a captured doc against a fixed certificate
// validity window.
type NitroVerifyOptions struct {
	Roots             *x509.CertPool
	ExpectedNonce     []byte
	ExpectedPublicKey []byte
	Now               time.Time
}

// VerifyNitroDocument verifies a Nitro COSE_Sign1 attestation document end to end:
//
//  1. parse the COSE_Sign1 + CBOR payload;
//  2. build and verify the certificate chain (leaf -> cabundle -> trusted root);
//  3. verify the COSE_Sign1 signature with the leaf certificate's public key
//     (Nitro uses ECDSA P-384 / ES384);
//  4. (optional) bind the nonce + public_key to the expected per-run values.
//
// On success it returns the decoded document so the caller can read the pinned PCR
// measurement, the bound public key, and the nonce. Any failure returns an error and
// NO accepted measurement — mirroring the backend's "reject -> no bundle, no key
// release" contract.
func VerifyNitroDocument(coseSign1 []byte, opts NitroVerifyOptions) (*NitroDocument, error) {
	doc, err := ParseNitroDocument(coseSign1)
	if err != nil {
		return nil, err
	}

	leaf, err := x509.ParseCertificate(doc.Certificate)
	if err != nil {
		return nil, fmt.Errorf("nitro: parse leaf certificate: %w", err)
	}

	intermediates := x509.NewCertPool()
	for _, der := range doc.CABundle {
		c, err := x509.ParseCertificate(der)
		if err != nil {
			return nil, fmt.Errorf("nitro: parse CA bundle certificate: %w", err)
		}
		intermediates.AddCert(c)
	}

	if opts.Roots == nil {
		return nil, errors.New("nitro: no trusted attestation root configured")
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:         opts.Roots,
		Intermediates: intermediates,
		CurrentTime:   now,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}); err != nil {
		return nil, fmt.Errorf("nitro: certificate chain does not verify to a trusted root: %w", err)
	}

	pub, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return nil, errors.New("nitro: leaf certificate key is not ECDSA (Nitro uses P-384/ES384)")
	}
	verifier, err := cose.NewVerifier(cose.AlgorithmES384, pub)
	if err != nil {
		return nil, fmt.Errorf("nitro: build COSE verifier: %w", err)
	}
	var msg cose.Sign1Message
	if err := msg.UnmarshalCBOR(coseSign1); err != nil {
		return nil, fmt.Errorf("nitro: re-decode COSE_Sign1: %w", err)
	}
	if err := msg.Verify(nil, verifier); err != nil {
		return nil, fmt.Errorf("nitro: COSE_Sign1 signature invalid (forged/tampered document): %w", err)
	}

	// Nonce + public-key binding: the document must carry exactly the per-run
	// challenge and ephemeral public key so a captured document cannot be replayed
	// and the released key is bound to THIS enclave + THIS run (§3 steps 2-4).
	if opts.ExpectedNonce != nil && !bytesEqualConstTime(doc.Nonce, opts.ExpectedNonce) {
		return nil, errors.New("nitro: attestation nonce does not match the challenge (replay rejected)")
	}
	if opts.ExpectedPublicKey != nil && !bytesEqualConstTime(doc.PublicKey, opts.ExpectedPublicKey) {
		return nil, errors.New("nitro: attestation public_key does not match the run's ephemeral key")
	}
	return doc, nil
}

// bytesEqualConstTime compares two byte slices in constant time once lengths match.
func bytesEqualConstTime(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := range a {
		v |= a[i] ^ b[i]
	}
	return v == 0
}
