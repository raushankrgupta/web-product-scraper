package utils

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/raushankrgupta/web-product-scraper/config"
)

// testAppleChain is a throwaway root → intermediate → leaf chain shaped like
// Apple's, so the verifier can be exercised without real App Store payloads.
type testAppleChain struct {
	root, intermediate, leaf *x509.Certificate
	leafKey                  *ecdsa.PrivateKey
	x5c                      []string
}

func newTestAppleChain(t *testing.T, leafOID, intermediateOID asn1.ObjectIdentifier) *testAppleChain {
	t.Helper()
	mk := func(tmpl, parent *x509.Certificate, pub *ecdsa.PublicKey, signer *ecdsa.PrivateKey) *x509.Certificate {
		der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, pub, signer)
		if err != nil {
			t.Fatal(err)
		}
		c, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	key := func() *ecdsa.PrivateKey {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	notBefore, notAfter := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)

	rootKey, intKey, leafKey := key(), key(), key()
	rootTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test Root"},
		NotBefore: notBefore, NotAfter: notAfter, IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign,
	}
	root := mk(rootTmpl, rootTmpl, &rootKey.PublicKey, rootKey)

	intTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "Test WWDR"},
		NotBefore: notBefore, NotAfter: notAfter, IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign,
	}
	if intermediateOID != nil {
		intTmpl.ExtraExtensions = []pkix.Extension{{Id: intermediateOID, Value: []byte{5, 0}}}
	}
	intermediate := mk(intTmpl, root, &intKey.PublicKey, rootKey)

	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(3), Subject: pkix.Name{CommonName: "Test StoreKit"},
		NotBefore: notBefore, NotAfter: notAfter, KeyUsage: x509.KeyUsageDigitalSignature,
	}
	if leafOID != nil {
		leafTmpl.ExtraExtensions = []pkix.Extension{{Id: leafOID, Value: []byte{5, 0}}}
	}
	leaf := mk(leafTmpl, intermediate, &leafKey.PublicKey, intKey)

	return &testAppleChain{
		root: root, intermediate: intermediate, leaf: leaf, leafKey: leafKey,
		x5c: []string{
			base64.StdEncoding.EncodeToString(leaf.Raw),
			base64.StdEncoding.EncodeToString(intermediate.Raw),
			base64.StdEncoding.EncodeToString(root.Raw),
		},
	}
}

func (c *testAppleChain) trust(t *testing.T) {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(c.root)
	prev := appleJWSRoots
	appleJWSRoots = func() (*x509.CertPool, error) { return pool, nil }
	t.Cleanup(func() { appleJWSRoots = prev })
}

func (c *testAppleChain) sign(t *testing.T, payload any) string {
	t.Helper()
	header, _ := json.Marshal(map[string]any{"alg": "ES256", "x5c": c.x5c})
	body, _ := json.Marshal(payload)
	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(body)
	digest := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, c.leafKey, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func TestAppleRootIsPinned(t *testing.T) {
	if _, err := appleRootPool(); err != nil {
		t.Fatalf("embedded Apple root must match its pinned fingerprint: %v", err)
	}
}

func TestVerifyAppleJWS_AcceptsGenuinePayload(t *testing.T) {
	chain := newTestAppleChain(t, oidAppleStoreKitSigning, oidAppleWWDRIntermediate)
	chain.trust(t)

	signed := chain.sign(t, map[string]any{"transactionId": "2000000123", "productId": "stars_40"})
	var txn AppleTransaction
	if err := VerifyAppleJWS(signed, &txn); err != nil {
		t.Fatalf("expected valid, got %v", err)
	}
	if txn.TransactionID != "2000000123" || txn.ProductID != "stars_40" {
		t.Fatalf("payload not decoded: %+v", txn)
	}
}

func TestVerifyAppleJWS_RejectsTamperedPayload(t *testing.T) {
	chain := newTestAppleChain(t, oidAppleStoreKitSigning, oidAppleWWDRIntermediate)
	chain.trust(t)

	signed := chain.sign(t, map[string]any{"productId": "stars_40"})
	parts := strings.Split(signed, ".")
	forged, _ := json.Marshal(map[string]any{"productId": "stars_1000"})
	parts[1] = base64.RawURLEncoding.EncodeToString(forged)

	if err := VerifyAppleJWS(strings.Join(parts, "."), nil); !errors.Is(err, ErrAppleJWSInvalid) {
		t.Fatalf("tampered payload must be rejected, got %v", err)
	}
}

func TestVerifyAppleJWS_RejectsUntrustedRoot(t *testing.T) {
	trusted := newTestAppleChain(t, oidAppleStoreKitSigning, oidAppleWWDRIntermediate)
	trusted.trust(t)
	attacker := newTestAppleChain(t, oidAppleStoreKitSigning, oidAppleWWDRIntermediate)

	if err := VerifyAppleJWS(attacker.sign(t, map[string]any{}), nil); !errors.Is(err, ErrAppleJWSInvalid) {
		t.Fatalf("chain to a different root must be rejected, got %v", err)
	}
}

func TestVerifyAppleJWS_RequiresAppleMarkerExtensions(t *testing.T) {
	noLeafOID := newTestAppleChain(t, nil, oidAppleWWDRIntermediate)
	noLeafOID.trust(t)
	if err := VerifyAppleJWS(noLeafOID.sign(t, map[string]any{}), nil); !errors.Is(err, ErrAppleJWSInvalid) {
		t.Fatalf("leaf without the StoreKit OID must be rejected, got %v", err)
	}

	noIntOID := newTestAppleChain(t, oidAppleStoreKitSigning, nil)
	noIntOID.trust(t)
	if err := VerifyAppleJWS(noIntOID.sign(t, map[string]any{}), nil); !errors.Is(err, ErrAppleJWSInvalid) {
		t.Fatalf("intermediate without the WWDR OID must be rejected, got %v", err)
	}
}

func TestAppAccountTokenRoundTrip(t *testing.T) {
	id := "64b7f0c2a1d3e4f5a6b7c8d9"
	tok := AppAccountTokenForUser(id)
	if tok != "64b7f0c2-a1d3-e4f5-a6b7-c8d900000000" {
		t.Fatalf("unexpected token %q", tok)
	}
	if got := UserIDFromAppAccountToken(strings.ToUpper(tok)); got != id {
		t.Fatalf("round trip: got %q", got)
	}
	for _, bad := range []string{"", "guest:abc", "not-a-uuid", "64b7f0c2-a1d3-e4f5-a6b7-c8d912345678"} {
		if UserIDFromAppAccountToken(bad) != "" {
			t.Fatalf("%q must not map to a user", bad)
		}
	}
	if AppAccountTokenForUser("guest:device") != "" {
		t.Fatal("non-ObjectID ids get no token")
	}
}

func TestGetAppleTransaction_FallsBackToSandbox(t *testing.T) {
	chain := newTestAppleChain(t, oidAppleStoreKitSigning, oidAppleWWDRIntermediate)
	chain.trust(t)

	signed := chain.sign(t, map[string]any{
		"transactionId": "42", "bundleId": "com.example.app", "environment": AppleEnvSandbox,
	})

	var calls []string
	prod := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, "prod "+r.URL.Path)
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Error("missing bearer token")
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errorCode":4040010}`))
	}))
	defer prod.Close()
	sandbox := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, "sandbox "+r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]string{"signedTransactionInfo": signed})
	}))
	defer sandbox.Close()

	prevURLs := appStoreBaseURLs
	appStoreBaseURLs = map[string]string{AppleEnvProduction: prod.URL, AppleEnvSandbox: sandbox.URL}
	t.Cleanup(func() { appStoreBaseURLs = prevURLs })
	withTestAppStoreKey(t)

	txn, err := GetAppleTransaction(context.Background(), "42")
	if err != nil {
		t.Fatalf("expected sandbox transaction, got %v", err)
	}
	if txn.Environment != AppleEnvSandbox || txn.TransactionID != "42" {
		t.Fatalf("unexpected transaction %+v", txn)
	}
	if len(calls) != 2 || !strings.HasPrefix(calls[0], "prod /inApps/v1/transactions/42") {
		t.Fatalf("production must be asked first, then sandbox: %v", calls)
	}

	// With sandbox switched off, the same id is simply not found.
	config.AppStoreAllowSandbox = false
	if _, err := GetAppleTransaction(context.Background(), "42"); !errors.Is(err, ErrAppleTransactionNotFound) {
		t.Fatalf("sandbox disabled must not fall back, got %v", err)
	}
}

func withTestAppStoreKey(t *testing.T) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	prev := struct {
		issuer, keyID, key, bundle string
		sandbox                    bool
	}{config.AppStoreIssuerID, config.AppStoreKeyID, config.AppStorePrivateKey, config.AppleBundleID, config.AppStoreAllowSandbox}

	config.AppStoreIssuerID = "issuer"
	config.AppStoreKeyID = "KEYID"
	config.AppStorePrivateKey = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	config.AppleBundleID = "com.example.app"
	config.AppStoreAllowSandbox = true
	appStoreTokenCache.Lock()
	appStoreTokenCache.token = ""
	appStoreTokenCache.Unlock()

	t.Cleanup(func() {
		config.AppStoreIssuerID, config.AppStoreKeyID, config.AppStorePrivateKey = prev.issuer, prev.keyID, prev.key
		config.AppleBundleID, config.AppStoreAllowSandbox = prev.bundle, prev.sandbox
		appStoreTokenCache.Lock()
		appStoreTokenCache.token = ""
		appStoreTokenCache.Unlock()
	})
}
