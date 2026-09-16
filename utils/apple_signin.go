package utils

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/raushankrgupta/web-product-scraper/config"
)

// Sign in with Apple, server side.
//
// The app hands over the identity token Apple issued on the device. It is a
// JWT signed with one of Apple's rotating RSA keys; verifying it against those
// keys, for our bundle id, is what proves the person controls that Apple
// account. The authorization code that comes with it is exchanged for a
// refresh token, which is kept for one purpose: revoking it when the account
// is deleted, which App Review requires (Apple TN3194).

const (
	appleIssuer    = "https://appleid.apple.com"
	appleKeysURL   = "https://appleid.apple.com/auth/keys"
	appleTokenURL  = "https://appleid.apple.com/auth/token"
	appleRevokeURL = "https://appleid.apple.com/auth/revoke"
)

// ErrAppleIdentityInvalid wraps every reason an identity token is refused.
var ErrAppleIdentityInvalid = errors.New("apple identity token invalid")

// AppleIdentity is what a verified identity token tells us.
type AppleIdentity struct {
	// Subject is the stable, app-scoped user id. It never changes, unlike the
	// email, which the user can hide and which Apple may omit.
	Subject        string
	Email          string
	EmailVerified  bool
	IsPrivateEmail bool
}

var appleSignInHTTP = &http.Client{Timeout: 10 * time.Second}

var appleKeys struct {
	sync.Mutex
	keys      map[string]*rsa.PublicKey
	fetchedAt time.Time
}

// appleKeyByID returns Apple's signing key for kid, refreshing the set when it
// is stale or the kid is new — Apple rotates keys without notice.
func appleKeyByID(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	appleKeys.Lock()
	defer appleKeys.Unlock()

	if k, ok := appleKeys.keys[kid]; ok && time.Since(appleKeys.fetchedAt) < 24*time.Hour {
		return k, nil
	}
	// An unknown kid triggers a refetch, but not more than once a minute, so a
	// flood of garbage tokens cannot turn into a flood of requests to Apple.
	if time.Since(appleKeys.fetchedAt) < time.Minute && appleKeys.keys != nil {
		if k, ok := appleKeys.keys[kid]; ok {
			return k, nil
		}
		return nil, fmt.Errorf("%w: unknown key id", ErrAppleIdentityInvalid)
	}

	keys, err := fetchAppleKeys(ctx)
	if err != nil {
		if k, ok := appleKeys.keys[kid]; ok {
			return k, nil // stale but still Apple's; better than failing sign-in
		}
		return nil, err
	}
	appleKeys.keys, appleKeys.fetchedAt = keys, time.Now()
	if k, ok := keys[kid]; ok {
		return k, nil
	}
	return nil, fmt.Errorf("%w: unknown key id", ErrAppleIdentityInvalid)
}

func fetchAppleKeys(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, appleKeysURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := appleSignInHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch apple keys: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch apple keys: status %d", resp.StatusCode)
	}

	var set struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&set); err != nil {
		return nil, fmt.Errorf("decode apple keys: %w", err)
	}

	out := make(map[string]*rsa.PublicKey, len(set.Keys))
	for _, k := range set.Keys {
		if k.Kty != "RSA" {
			continue
		}
		nb, errN := base64.RawURLEncoding.DecodeString(k.N)
		eb, errE := base64.RawURLEncoding.DecodeString(k.E)
		if errN != nil || errE != nil {
			continue
		}
		out[k.Kid] = &rsa.PublicKey{
			N: new(big.Int).SetBytes(nb),
			E: int(new(big.Int).SetBytes(eb).Int64()),
		}
	}
	if len(out) == 0 {
		return nil, errors.New("apple returned no usable keys")
	}
	return out, nil
}

// VerifyAppleIdentityToken checks an identity token issued for this app.
func VerifyAppleIdentityToken(ctx context.Context, identityToken string) (*AppleIdentity, error) {
	claims := jwt.MapClaims{}
	_, err := jwt.ParseWithClaims(strings.TrimSpace(identityToken), claims,
		func(t *jwt.Token) (interface{}, error) {
			kid, _ := t.Header["kid"].(string)
			if kid == "" {
				return nil, fmt.Errorf("%w: no key id", ErrAppleIdentityInvalid)
			}
			return appleKeyByID(ctx, kid)
		},
		jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithIssuer(appleIssuer),
		jwt.WithAudience(config.AppleBundleID),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(time.Minute),
	)
	if err != nil {
		if errors.Is(err, ErrAppleIdentityInvalid) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %v", ErrAppleIdentityInvalid, err)
	}

	sub, _ := claims["sub"].(string)
	if sub == "" {
		return nil, fmt.Errorf("%w: no subject", ErrAppleIdentityInvalid)
	}
	email, _ := claims["email"].(string)
	return &AppleIdentity{
		Subject:        sub,
		Email:          strings.TrimSpace(email),
		EmailVerified:  claimTrue(claims["email_verified"]),
		IsPrivateEmail: claimTrue(claims["is_private_email"]),
	}, nil
}

// claimTrue reads a boolean claim Apple sends as either true or "true".
func claimTrue(v interface{}) bool {
	switch b := v.(type) {
	case bool:
		return b
	case string:
		return strings.EqualFold(strings.TrimSpace(b), "true")
	}
	return false
}

// AppleSignInRevokeConfigured reports whether authorization codes can be
// exchanged and tokens revoked.
func AppleSignInRevokeConfigured() bool {
	return config.AppleTeamID != "" && config.AppleSignInKeyID != "" && config.AppleSignInPrivKey != ""
}

// appleClientSecret is the short-lived JWT Apple accepts in place of a client
// secret on its token and revoke endpoints.
func appleClientSecret() (string, error) {
	key, err := parseApplePrivateKey(config.AppleSignInPrivKey)
	if err != nil {
		return "", fmt.Errorf("sign in with apple key: %w", err)
	}
	now := time.Now()
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"iss": config.AppleTeamID,
		"iat": now.Unix(),
		"exp": now.Add(5 * time.Minute).Unix(),
		"aud": appleIssuer,
		"sub": config.AppleBundleID,
	})
	tok.Header["kid"] = config.AppleSignInKeyID
	return tok.SignedString(key)
}

// ExchangeAppleAuthCode trades a one-time authorization code for a refresh
// token. The code is valid for five minutes, so this runs during sign-in.
func ExchangeAppleAuthCode(ctx context.Context, code string) (string, error) {
	if !AppleSignInRevokeConfigured() {
		return "", errors.New("sign in with apple key not configured")
	}
	secret, err := appleClientSecret()
	if err != nil {
		return "", err
	}
	form := url.Values{
		"client_id":     {config.AppleBundleID},
		"client_secret": {secret},
		"code":          {strings.TrimSpace(code)},
		"grant_type":    {"authorization_code"},
	}
	body, err := postAppleForm(ctx, appleTokenURL, form)
	if err != nil {
		return "", err
	}
	var resp struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("decode apple token response: %w", err)
	}
	if resp.RefreshToken == "" {
		return "", errors.New("apple token response had no refresh token")
	}
	return resp.RefreshToken, nil
}

// RevokeAppleToken invalidates a user's Sign in with Apple grant, which
// removes the app from their Apple ID's "Sign in with Apple" list.
func RevokeAppleToken(ctx context.Context, refreshToken string) error {
	if !AppleSignInRevokeConfigured() {
		return errors.New("sign in with apple key not configured")
	}
	secret, err := appleClientSecret()
	if err != nil {
		return err
	}
	form := url.Values{
		"client_id":       {config.AppleBundleID},
		"client_secret":   {secret},
		"token":           {refreshToken},
		"token_type_hint": {"refresh_token"},
	}
	_, err = postAppleForm(ctx, appleRevokeURL, form)
	return err
}

func postAppleForm(ctx context.Context, endpoint string, form url.Values) ([]byte, error) {
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := appleSignInHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apple %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("apple %s returned %d: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}
