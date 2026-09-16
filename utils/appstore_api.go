package utils

import (
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/raushankrgupta/web-product-scraper/config"
)

// App Store Server API client — the iOS counterpart of play_billing.go.
//
// A purchase on an iPhone is confirmed by asking Apple for the transaction by
// id, never by trusting what the app sends. The response is itself a JWS, and
// it is verified against Apple's root like everything else Apple signs.

// App Store environments, as Apple spells them in payloads.
const (
	AppleEnvProduction = "Production"
	AppleEnvSandbox    = "Sandbox"
)

// appStoreBaseURLs is a variable so tests can point it at a local server.
var appStoreBaseURLs = map[string]string{
	AppleEnvProduction: "https://api.storekit.itunes.apple.com",
	AppleEnvSandbox:    "https://api.storekit-sandbox.itunes.apple.com",
}

// ErrAppleTransactionNotFound means neither environment knows the id.
var ErrAppleTransactionNotFound = errors.New("apple transaction not found")

// AppleTransaction is the decoded JWSTransactionDecodedPayload. Only the
// fields this backend acts on are listed.
type AppleTransaction struct {
	TransactionID         string `json:"transactionId"`
	OriginalTransactionID string `json:"originalTransactionId"`
	BundleID              string `json:"bundleId"`
	ProductID             string `json:"productId"`
	Type                  string `json:"type"` // "Consumable" for star packs
	Quantity              int    `json:"quantity"`
	PurchaseDate          int64  `json:"purchaseDate"`
	SignedDate            int64  `json:"signedDate"`
	AppAccountToken       string `json:"appAccountToken"`
	Environment           string `json:"environment"`
	Storefront            string `json:"storefront"`
	Currency              string `json:"currency"`
	Price                 int64  `json:"price"` // milli-units of Currency
	RevocationDate        int64  `json:"revocationDate"`
	RevocationReason      *int   `json:"revocationReason"`
	TransactionReason     string `json:"transactionReason"`
}

// AppStoreConfigured reports whether iOS purchases can be verified at all.
func AppStoreConfigured() bool {
	return config.AppStoreIssuerID != "" && config.AppStoreKeyID != "" &&
		config.AppStorePrivateKey != "" && config.AppleBundleID != ""
}

var appStoreHTTP = &http.Client{Timeout: 15 * time.Second}

// appStoreTokenCache reuses the API JWT for most of its life. Apple accepts a
// token for up to an hour; minting one per request is wasted signing.
var appStoreTokenCache struct {
	sync.Mutex
	token   string
	expires time.Time
}

func appStoreToken() (string, error) {
	appStoreTokenCache.Lock()
	defer appStoreTokenCache.Unlock()
	if appStoreTokenCache.token != "" && time.Until(appStoreTokenCache.expires) > 2*time.Minute {
		return appStoreTokenCache.token, nil
	}

	key, err := parseApplePrivateKey(config.AppStorePrivateKey)
	if err != nil {
		return "", fmt.Errorf("app store key: %w", err)
	}
	now := time.Now()
	exp := now.Add(20 * time.Minute)
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"iss": config.AppStoreIssuerID,
		"iat": now.Unix(),
		"exp": exp.Unix(),
		"aud": "appstoreconnect-v1",
		"bid": config.AppleBundleID,
	})
	tok.Header["kid"] = config.AppStoreKeyID
	signed, err := tok.SignedString(key)
	if err != nil {
		return "", fmt.Errorf("sign app store token: %w", err)
	}
	appStoreTokenCache.token, appStoreTokenCache.expires = signed, exp
	return signed, nil
}

// parseApplePrivateKey reads a .p8 (PKCS#8 EC P-256) key as issued by Apple.
func parseApplePrivateKey(pemText string) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemText))
	if block == nil {
		return nil, errors.New("not PEM — expected the contents of the .p8 file")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse PKCS#8: %w", err)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("key is not ECDSA")
	}
	return key, nil
}

// GetAppleTransaction fetches and verifies a transaction by id.
//
// Production is asked first and sandbox second. That order is not optional:
// App Review installs the production build and pays with sandbox accounts, so
// a server that only asks production rejects every purchase the reviewer makes
// and the app is rejected with "unable to complete purchase".
func GetAppleTransaction(ctx context.Context, transactionID string) (*AppleTransaction, error) {
	transactionID = strings.TrimSpace(transactionID)
	if transactionID == "" {
		return nil, fmt.Errorf("%w: empty id", ErrAppleTransactionNotFound)
	}
	if !AppStoreConfigured() {
		return nil, errors.New("app store server api is not configured")
	}

	envs := []string{AppleEnvProduction}
	if config.AppStoreAllowSandbox {
		envs = append(envs, AppleEnvSandbox)
	}

	for _, env := range envs {
		txn, err := getAppleTransactionFrom(ctx, env, transactionID)
		if errors.Is(err, ErrAppleTransactionNotFound) {
			continue
		}
		return txn, err
	}
	return nil, ErrAppleTransactionNotFound
}

func getAppleTransactionFrom(ctx context.Context, env, transactionID string) (*AppleTransaction, error) {
	token, err := appStoreToken()
	if err != nil {
		return nil, err
	}

	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	endpoint := appStoreBaseURLs[env] + "/inApps/v1/transactions/" + url.PathEscape(transactionID)
	req, err := http.NewRequestWithContext(callCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := appStoreHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("app store %s: %w", env, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		// 4040010 TransactionIdNotFoundError — normal for the wrong
		// environment. Any other 404 is treated the same way: there is no
		// transaction to credit behind it either.
		return nil, ErrAppleTransactionNotFound
	case http.StatusBadRequest:
		// An id that is not even well-formed cannot be a real purchase.
		return nil, fmt.Errorf("%w: %s", ErrAppleTransactionNotFound, strings.TrimSpace(string(body)))
	case http.StatusUnauthorized:
		// Our credentials are wrong — never the user's fault, and never a
		// reason to reject their purchase.
		return nil, fmt.Errorf("app store %s rejected our credentials (check APPSTORE_* keys)", env)
	default:
		return nil, fmt.Errorf("app store %s returned %d: %s", env, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var envelope struct {
		SignedTransactionInfo string `json:"signedTransactionInfo"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.SignedTransactionInfo == "" {
		return nil, fmt.Errorf("app store %s: unexpected response body", env)
	}

	var txn AppleTransaction
	if err := VerifyAppleJWS(envelope.SignedTransactionInfo, &txn); err != nil {
		return nil, err
	}
	return &txn, nil
}

// AppleNotification is the decoded App Store Server Notification V2 payload.
type AppleNotification struct {
	NotificationType string `json:"notificationType"`
	Subtype          string `json:"subtype"`
	NotificationUUID string `json:"notificationUUID"`
	Version          string `json:"version"`
	SignedDate       int64  `json:"signedDate"`
	Data             struct {
		AppAppleID            int64  `json:"appAppleId"`
		BundleID              string `json:"bundleId"`
		Environment           string `json:"environment"`
		SignedTransactionInfo string `json:"signedTransactionInfo"`
	} `json:"data"`

	// Transaction is data.signedTransactionInfo, verified and decoded. Nil for
	// notification types that carry no transaction (TEST).
	Transaction *AppleTransaction `json:"-"`
}

// DecodeAppleNotification verifies a signedPayload and the transaction inside it.
func DecodeAppleNotification(signedPayload string) (*AppleNotification, error) {
	var n AppleNotification
	if err := VerifyAppleJWS(signedPayload, &n); err != nil {
		return nil, err
	}
	if n.Data.SignedTransactionInfo != "" {
		var txn AppleTransaction
		if err := VerifyAppleJWS(n.Data.SignedTransactionInfo, &txn); err != nil {
			return nil, err
		}
		n.Transaction = &txn
	}
	return &n, nil
}

// AppAccountTokenForUser derives the UUID the app stamps on an iOS purchase.
//
// StoreKit's appAccountToken must be a UUID, and user ids are 24-hex Mongo
// ObjectIDs, so the id is padded with eight zeros to 32 hex digits and
// formatted as one. The mapping is reversible, which is the whole point: a
// notification for a purchase the app never managed to submit can still be
// credited to the person who paid. Must match appAccountTokenFor in the app's
// BillingService.
func AppAccountTokenForUser(userID string) string {
	id := strings.ToLower(strings.TrimSpace(userID))
	if len(id) != 24 || !isHex(id) {
		return ""
	}
	h := id + "00000000"
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// UserIDFromAppAccountToken reverses AppAccountTokenForUser. It returns "" for
// any token that is not one of ours.
func UserIDFromAppAccountToken(token string) string {
	h := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(token), "-", ""))
	if len(h) != 32 || !isHex(h) || h[24:] != "00000000" {
		return ""
	}
	return h[:24]
}

func isHex(s string) bool {
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
