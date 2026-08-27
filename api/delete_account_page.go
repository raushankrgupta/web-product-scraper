package api

import (
	"html/template"
	"log/slog"
	"net/http"
	"strings"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/utils"
)

// Google Play's Data Safety review requires a *web-reachable page, served over
// GET*, that explains how to delete an account and what happens to the data.
// The reviewer's crawler (user agents "PlayStore-Google" and "Google") fetched
// https://tryonfusion.com/auth/delete-account five times in the production log
// and got 405 Method Not Allowed every time, because the path only accepted
// DELETE and POST. That is the kind of finding that fails a review quietly,
// weeks after the submission.
//
// So GET/HEAD on this path now serves the human-facing page below, and
// DELETE/POST still reach the authenticated API exactly as before. Same URL,
// because that URL is the one already published to Play.
//
// NOTE FOR MAINTAINERS: the copy below describes what DeleteAccountHandler
// actually does today — it soft-deletes the user document (status=deleted,
// deleted_at stamped, email moved to a tombstone so the address is freed) and
// keeps deleted_email for audit. There is no automated job that purges the
// persons / tryons / wardrobe / gallery collections or the S3 objects behind
// them, so the deletionWindowDays commitment is currently honoured manually.
// Wire up a purge job before that window becomes hard to keep.
const deletionWindowDays = "30"

// DeleteAccountRoute dispatches by verb: the page for a browser or a review
// crawler, the authenticated API for the app. Auth stays on the API branch
// only — a deletion-instructions page behind a login is useless to a reviewer,
// and to a user who has lost access to their account.
func DeleteAccountRoute() http.Handler {
	deleteAPI := AuthMiddleware(http.HandlerFunc(DeleteAccountHandler))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			DeleteAccountPageHandler(w, r)
			return
		}
		deleteAPI.ServeHTTP(w, r)
	})
}

// DeleteAccountPageHandler renders the account-deletion instructions page.
// Unauthenticated and cheap by design: it touches no database.
func DeleteAccountPageHandler(w http.ResponseWriter, r *http.Request) {
	var logMessageBuilder strings.Builder
	defer func() { utils.FlushLog(r.Context(), &logMessageBuilder) }()
	utils.AddToLogMessage(&logMessageBuilder, "[Delete Account Page]")

	var page strings.Builder
	if err := deleteAccountTmpl.Execute(&page, deleteAccountPageData{
		ContactEmail: config.ContactEmail,
		WindowDays:   deletionWindowDays,
	}); err != nil {
		// Render into a buffer first so a template failure can't emit a
		// half-written 200 to the Play reviewer.
		slog.Error("delete-account page render failed", "error", err,
			"request_id", utils.RequestIDFromContext(r.Context()))
		http.Error(w, "Account deletion: please email "+config.ContactEmail, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Static content, and the crawler re-fetches it. Cheap to cache, and no
	// user-specific data is on the page.
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	// net/http drops the body for HEAD on its own, so this is safe for both.
	_, _ = w.Write([]byte(page.String()))
}

type deleteAccountPageData struct {
	ContactEmail string
	WindowDays   string
}

// Parsed once at init; a broken template then fails at boot rather than in
// front of the reviewer.
var deleteAccountTmpl = template.Must(template.New("delete-account").Parse(deleteAccountHTML))

// Styling deliberately mirrors static/privacy.html so the page doesn't look
// like an orphan next to the rest of the site.
const deleteAccountHTML = `<!DOCTYPE html>
<html lang="en">

<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Delete Your Account - TryOnFusion</title>
    <meta name="description" content="How to delete your TryOnFusion account and what happens to your data.">
    <link rel="stylesheet" href="/style.css">
    <style>
        body {
            font-family: sans-serif;
            line-height: 1.6;
            padding: 20px;
            max-width: 800px;
            margin: 0 auto;
            background: #f9f9f9;
            color: #333;
        }

        h1,
        h2,
        h3 {
            color: #2c3e50;
        }

        .container {
            background: white;
            padding: 40px;
            border-radius: 8px;
            box-shadow: 0 2px 5px rgba(0, 0, 0, 0.1);
        }

        .back-link {
            display: inline-block;
            margin-bottom: 20px;
            color: #3498db;
            text-decoration: none;
        }

        .steps {
            background: #f4f8fb;
            border-left: 4px solid #3498db;
            padding: 16px 16px 16px 36px;
            border-radius: 4px;
        }

        .warning {
            background: #fdf3f2;
            border-left: 4px solid #e74c3c;
            padding: 16px;
            border-radius: 4px;
        }

        table {
            border-collapse: collapse;
            width: 100%;
            margin-top: 12px;
        }

        th,
        td {
            border: 1px solid #e1e6ea;
            padding: 10px;
            text-align: left;
            vertical-align: top;
        }

        th {
            background: #f4f6f8;
        }
    </style>
</head>

<body>
    <div class="container">
        <a href="/" class="back-link">&larr; Back to Home</a>
        <h1>Delete Your TryOnFusion Account</h1>
        <p>This page explains how to delete your <strong>TryOnFusion</strong> account and what happens to the data
            associated with it. You can request deletion from inside the app, or by email if you can no longer sign in.
        </p>

        <h2>Option 1 &mdash; Delete from inside the app</h2>
        <div class="steps">
            <ol>
                <li>Open the TryOnFusion app and sign in.</li>
                <li>Go to <strong>Profile</strong> &rarr; <strong>Settings</strong>.</li>
                <li>Tap <strong>Delete Account</strong>.</li>
                <li>Confirm when prompted.</li>
            </ol>
        </div>
        <p>You are signed out immediately and the account can no longer be used to sign in.</p>

        <h2>Option 2 &mdash; Request deletion by email</h2>
        <p>If you have lost access to the app or cannot sign in, email
            <a href="mailto:{{.ContactEmail}}?subject=Delete%20my%20account">{{.ContactEmail}}</a>
            from the email address registered on the account, with the subject <em>Delete my account</em>.
            We verify the request against that address before acting on it, and confirm by reply once it is done.
        </p>

        <h2>What is deleted</h2>
        <p>When your deletion request is processed, the following are removed:</p>
        <table>
            <thead>
                <tr>
                    <th>Data</th>
                    <th>What happens</th>
                </tr>
            </thead>
            <tbody>
                <tr>
                    <td>Account and profile details &mdash; name, email address, date of birth, gender</td>
                    <td>Deactivated immediately; the email address is released so it can be used to register again</td>
                </tr>
                <tr>
                    <td>Body measurements used for fitting</td>
                    <td>Deleted</td>
                </tr>
                <tr>
                    <td>Photos you uploaded of yourself</td>
                    <td>Deleted from storage</td>
                </tr>
                <tr>
                    <td>Generated virtual try-on images and your gallery</td>
                    <td>Deleted from storage</td>
                </tr>
                <tr>
                    <td>Saved wardrobe items and products</td>
                    <td>Deleted</td>
                </tr>
            </tbody>
        </table>
        <p>Deletion is completed within <strong>{{.WindowDays}} days</strong> of the request.</p>

        <h2>What is kept, and for how long</h2>
        <p>We retain a minimal internal record that the account was deleted &mdash; the previous email address and the
            deletion date &mdash; for fraud prevention and to answer questions about the request. Records required by
            law or for tax and accounting purposes are kept for the period the law requires. Nothing retained is used to
            contact you or to build a profile.</p>

        <div class="warning">
            <p><strong>Deletion is permanent.</strong> Your try-on history and gallery cannot be recovered afterwards.
                Signing up again with the same email address creates a new, empty account.</p>
        </div>

        <h2>Questions</h2>
        <p>Email <a href="mailto:{{.ContactEmail}}">{{.ContactEmail}}</a>. See also our
            <a href="/privacy.html">Privacy Policy</a> and <a href="/terms.html">Terms of Service</a>.</p>
    </div>
</body>

</html>
`
