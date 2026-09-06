package utils

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/raushankrgupta/web-product-scraper/config"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/androidpublisher/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

// Google's purchaseState values for a one-time product.
//
// PENDING is the one that matters in India: UPI and netbanking purchases come
// back pending and settle asynchronously, sometimes minutes later. Crediting
// stars on a pending token hands them to anyone who opens the payment sheet
// and walks away, so pending is recorded and credited only once Play confirms.
const (
	PlayStatePurchased = 0
	PlayStateCancelled = 1
	PlayStatePending   = 2
)

// Consumption / acknowledgement states.
const (
	PlayNotConsumed     = 0
	PlayConsumed        = 1
	PlayNotAcknowledged = 0
	PlayAcknowledged    = 1
)

// ErrPlayBillingDisabled is returned when no service-account credentials are
// configured. Purchases are then refused outright rather than credited
// unverified — an unverified credit is free stars for anyone who can POST.
var ErrPlayBillingDisabled = errors.New("play billing is not configured")

// ErrPurchaseNotFound is returned for a token Google does not recognise.
var ErrPurchaseNotFound = errors.New("purchase token not found")

var (
	playOnce    sync.Once
	playService *androidpublisher.Service
	playErr     error
)

// playClient lazily builds the Android Publisher client from the configured
// service account. Built once for the process: the token exchange is not
// something to repeat per purchase.
func playClient(ctx context.Context) (*androidpublisher.Service, error) {
	playOnce.Do(func() {
		var opts []option.ClientOption

		params := google.CredentialsParams{Scopes: []string{androidpublisher.AndroidpublisherScope}}
		switch {
		case strings.TrimSpace(config.PlayServiceAccountJSON) != "":
			// Inline JSON is the deployment-friendly form: the whole key
			// lives in one env var, so nothing has to be mounted into the
			// container.
			creds, err := google.CredentialsFromJSONWithTypeAndParams(context.Background(), []byte(config.PlayServiceAccountJSON), google.ServiceAccount, params)
			if err != nil {
				playErr = fmt.Errorf("parse PLAY_SERVICE_ACCOUNT_JSON: %w", err)
				return
			}
			opts = append(opts, option.WithTokenSource(creds.TokenSource))
		case strings.TrimSpace(config.PlayServiceAccountFile) != "":
			data, err := os.ReadFile(config.PlayServiceAccountFile)
			if err != nil {
				playErr = fmt.Errorf("PLAY_SERVICE_ACCOUNT_FILE %q is unreadable: %w", config.PlayServiceAccountFile, err)
				return
			}
			creds, err := google.CredentialsFromJSONWithTypeAndParams(context.Background(), data, google.ServiceAccount, params)
			if err != nil {
				playErr = fmt.Errorf("parse PLAY_SERVICE_ACCOUNT_FILE: %w", err)
				return
			}
			opts = append(opts, option.WithTokenSource(creds.TokenSource))
		default:
			playErr = ErrPlayBillingDisabled
			return
		}

		opts = append(opts, option.WithScopes(androidpublisher.AndroidpublisherScope))

		// Deliberately not the request context — the client outlives the
		// request that happened to build it.
		playService, playErr = androidpublisher.NewService(context.Background(), opts...)
		if playErr != nil {
			playErr = fmt.Errorf("build android publisher client: %w", playErr)
			return
		}
		slog.Info("play billing client ready", "package", config.Stars.Billing.PackageName)
	})
	return playService, playErr
}

// PlayBillingConfigured reports whether purchases can be verified at all.
// /health surfaces this so a misconfigured deploy is visible before a user
// pays and gets nothing.
func PlayBillingConfigured() bool {
	return strings.TrimSpace(config.PlayServiceAccountJSON) != "" ||
		strings.TrimSpace(config.PlayServiceAccountFile) != ""
}

// VerifyPurchase asks Google about a purchase token. This is the only source
// of truth for whether a purchase happened — the client's word is never
// enough, because anyone can POST a made-up token.
func VerifyPurchase(ctx context.Context, productID, token string) (*androidpublisher.ProductPurchase, error) {
	svc, err := playClient(ctx)
	if err != nil {
		return nil, err
	}

	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	p, err := svc.Purchases.Products.
		Get(config.Stars.Billing.PackageName, productID, token).
		Context(callCtx).Do()
	if err != nil {
		var gerr *googleapi.Error
		if errors.As(err, &gerr) && (gerr.Code == 404 || gerr.Code == 410) {
			return nil, fmt.Errorf("%w: %s", ErrPurchaseNotFound, productID)
		}
		return nil, fmt.Errorf("verify purchase: %w", err)
	}
	return p, nil
}

// ConsumePurchase marks a consumable as used so the same product can be
// bought again.
//
// This also satisfies Play's acknowledgement requirement. Not doing it within
// three days makes Google automatically refund the user while we keep the
// stars on their balance — we would have given the product away and taken a
// chargeback for it. Consuming server-side rather than relying on the client
// means it happens exactly once and survives the app being killed mid-flow.
func ConsumePurchase(ctx context.Context, productID, token string) error {
	svc, err := playClient(ctx)
	if err != nil {
		return err
	}

	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	err = svc.Purchases.Products.
		Consume(config.Stars.Billing.PackageName, productID, token).
		Context(callCtx).Do()
	if err == nil {
		return nil
	}

	// An already-consumed token is success, not failure: it means a previous
	// attempt got further than it managed to report.
	var gerr *googleapi.Error
	if errors.As(err, &gerr) && gerr.Code == 400 &&
		strings.Contains(strings.ToLower(gerr.Message), "already been consumed") {
		return nil
	}
	return fmt.Errorf("consume purchase: %w", err)
}

// AcknowledgePurchase is the fallback when consumption is left to the client.
// Kept because the three-day auto-refund applies either way.
func AcknowledgePurchase(ctx context.Context, productID, token string) error {
	svc, err := playClient(ctx)
	if err != nil {
		return err
	}

	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	err = svc.Purchases.Products.
		Acknowledge(config.Stars.Billing.PackageName, productID,
			token, &androidpublisher.ProductPurchasesAcknowledgeRequest{}).
		Context(callCtx).Do()
	if err != nil {
		var gerr *googleapi.Error
		if errors.As(err, &gerr) && gerr.Code == 400 {
			return nil // already acknowledged
		}
		return fmt.Errorf("acknowledge purchase: %w", err)
	}
	return nil
}

// ListVoidedPurchases returns refunds and chargebacks Google has recorded
// since `since`.
//
// This is the polling alternative to Pub/Sub notifications. It is the simpler
// of the two to operate and is enough at low volume: a refund that is
// reconciled an hour late costs nothing, whereas an un-reconciled one leaves
// a user holding stars they were paid back for.
func ListVoidedPurchases(ctx context.Context, since time.Time) ([]*androidpublisher.VoidedPurchase, error) {
	svc, err := playClient(ctx)
	if err != nil {
		return nil, err
	}

	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var out []*androidpublisher.VoidedPurchase
	token := ""
	for {
		call := svc.Purchases.Voidedpurchases.
			List(config.Stars.Billing.PackageName).
			StartTime(since.UnixMilli()).
			MaxResults(1000).
			Context(callCtx)
		if token != "" {
			call = call.Token(token)
		}

		resp, err := call.Do()
		if err != nil {
			return out, fmt.Errorf("list voided purchases: %w", err)
		}
		out = append(out, resp.VoidedPurchases...)

		if resp.TokenPagination == nil || resp.TokenPagination.NextPageToken == "" {
			return out, nil
		}
		token = resp.TokenPagination.NextPageToken
	}
}
