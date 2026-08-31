package utils

import "time"

// utcDateString returns today's date in UTC as YYYY-MM-DD. Using UTC (vs
// server local) keeps the free-allowance window consistent across deploys and
// regions.
func utcDateString() string {
	return time.Now().UTC().Format("2006-01-02")
}

// QuotaStatus is the legacy daily-quota shape.
//
// The daily quota it described was replaced by the star economy (utils/stars.go
// and config/stars.json). The struct survives because app builds already in
// users' hands read `quota.remaining` from /billing/status to decide whether
// to grey out the try-on button; api.synthesiseLegacyQuota renders the star
// state into this shape so those clients keep behaving correctly. Nothing
// writes a tryon_quota document any more.
//
// It can be deleted once no released app build reads it.
type QuotaStatus struct {
	Plan      string `json:"plan"`
	Limit     int    `json:"limit"` // 0 == unlimited
	Used      int    `json:"used"`
	Remaining int    `json:"remaining"` // -1 == unlimited
	Date      string `json:"date"`
}
