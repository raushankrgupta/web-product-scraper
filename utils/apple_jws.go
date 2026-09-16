package utils

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	_ "embed"
)

// Everything Apple signs for the App Store — a transaction, a notification,
// renewal info — arrives as a JWS whose header carries the certificate chain
// (x5c) that signed it. Trusting that JWS means proving three things, and all
// three are required: the chain ends at Apple's root, the certificates are the
// App Store's own (not any certificate Apple ever issued), and the signature
// over this exact payload was made by the leaf.
//
// Apple ships libraries that do this for Swift, Java, Python and Node, but not
// Go, so it is implemented here against the standard library.

// appleRootG3DER is Apple Root CA - G3, downloaded from
// https://www.apple.com/certificateauthority/AppleRootCA-G3.cer.
//
//go:embed appleroot/AppleRootCA-G3.cer
var appleRootG3DER []byte

// appleRootG3SHA256 pins the embedded root. If the file is ever replaced by
// something else, verification refuses to start rather than trusting it.
const appleRootG3SHA256 = "63343abfb89a6a03ebb57e9b3f5fa7be7c4f5c756f3017b3a8c488c3653e9179"

// Marker extensions Apple puts on App Store signing certificates. Checking
// them is what stops a certificate Apple issued for some unrelated purpose —
// which would also chain to the root — from being used to forge a purchase.
var (
	oidAppleWWDRIntermediate = asn1.ObjectIdentifier{1, 2, 840, 113635, 100, 6, 2, 1}
	oidAppleStoreKitSigning  = asn1.ObjectIdentifier{1, 2, 840, 113635, 100, 6, 11, 1}
)

// ErrAppleJWSInvalid wraps every reason a signed payload is refused.
var ErrAppleJWSInvalid = errors.New("apple jws invalid")

var appleRootPool = sync.OnceValues(func() (*x509.CertPool, error) {
	sum := sha256.Sum256(appleRootG3DER)
	if hex.EncodeToString(sum[:]) != appleRootG3SHA256 {
		return nil, errors.New("embedded Apple root certificate does not match its pinned fingerprint")
	}
	root, err := x509.ParseCertificate(appleRootG3DER)
	if err != nil {
		return nil, fmt.Errorf("parse Apple root certificate: %w", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(root)
	return pool, nil
})

// appleJWSRoots returns the trust anchors. A variable so tests can sign with a
// throwaway chain; production code never reassigns it.
var appleJWSRoots = appleRootPool

type appleJWSHeader struct {
	Alg string   `json:"alg"`
	X5C []string `json:"x5c"`
}

// VerifyAppleJWS checks a JWS signed by the App Store and decodes its payload
// into out. It returns ErrAppleJWSInvalid (wrapped) for anything that is not a
// genuine, intact App Store payload.
func VerifyAppleJWS(signed string, out any) error {
	parts := strings.Split(strings.TrimSpace(signed), ".")
	if len(parts) != 3 {
		return fmt.Errorf("%w: not a compact JWS", ErrAppleJWSInvalid)
	}

	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return fmt.Errorf("%w: header encoding", ErrAppleJWSInvalid)
	}
	var header appleJWSHeader
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return fmt.Errorf("%w: header json", ErrAppleJWSInvalid)
	}
	if header.Alg != "ES256" {
		return fmt.Errorf("%w: unexpected alg %q", ErrAppleJWSInvalid, header.Alg)
	}
	if len(header.X5C) < 2 {
		return fmt.Errorf("%w: certificate chain too short", ErrAppleJWSInvalid)
	}

	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return fmt.Errorf("%w: payload encoding", ErrAppleJWSInvalid)
	}

	certs := make([]*x509.Certificate, 0, len(header.X5C))
	for _, c := range header.X5C {
		der, err := base64.StdEncoding.DecodeString(c)
		if err != nil {
			return fmt.Errorf("%w: x5c encoding", ErrAppleJWSInvalid)
		}
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			return fmt.Errorf("%w: x5c certificate", ErrAppleJWSInvalid)
		}
		certs = append(certs, cert)
	}
	leaf, intermediate := certs[0], certs[1]

	if !hasExtension(leaf, oidAppleStoreKitSigning) {
		return fmt.Errorf("%w: leaf is not an App Store signing certificate", ErrAppleJWSInvalid)
	}
	if !hasExtension(intermediate, oidAppleWWDRIntermediate) {
		return fmt.Errorf("%w: intermediate is not Apple WWDR", ErrAppleJWSInvalid)
	}

	roots, err := appleJWSRoots()
	if err != nil {
		return err
	}
	intermediates := x509.NewCertPool()
	intermediates.AddCert(intermediate)

	// Verify the chain as of when Apple signed the payload. A transaction from
	// last year is still genuine even if the certificate that signed it has
	// since expired; the signature below is what binds the date to the data.
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:         roots,
		Intermediates: intermediates,
		CurrentTime:   signedAt(payloadJSON),
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}); err != nil {
		return fmt.Errorf("%w: chain: %v", ErrAppleJWSInvalid, err)
	}

	pub, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return fmt.Errorf("%w: leaf key is not ECDSA", ErrAppleJWSInvalid)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		return fmt.Errorf("%w: signature encoding", ErrAppleJWSInvalid)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(pub, digest[:], r, s) {
		return fmt.Errorf("%w: signature does not match", ErrAppleJWSInvalid)
	}

	if out != nil {
		if err := json.Unmarshal(payloadJSON, out); err != nil {
			return fmt.Errorf("%w: payload json: %v", ErrAppleJWSInvalid, err)
		}
	}
	return nil
}

func hasExtension(cert *x509.Certificate, oid asn1.ObjectIdentifier) bool {
	for _, ext := range cert.Extensions {
		if ext.Id.Equal(oid) {
			return true
		}
	}
	return false
}

// signedAt reads signedDate (milliseconds) from a payload, falling back to now.
func signedAt(payload []byte) time.Time {
	var p struct {
		SignedDate int64 `json:"signedDate"`
	}
	if json.Unmarshal(payload, &p) == nil && p.SignedDate > 0 {
		return time.UnixMilli(p.SignedDate)
	}
	return time.Now()
}
