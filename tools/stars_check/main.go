// Command stars_check validates config/stars.json and prints the per-tier
// economics implied by it.
//
//	go run ./tools/stars_check
//
// It exits non-zero when any tier is priced below its margin floor, so it can
// run in CI and block a repricing that would lose money on every generation.
package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/raushankrgupta/web-product-scraper/config"
)

func main() {
	if err := config.LoadStars(); err != nil {
		fmt.Fprintf(os.Stderr, "star config is invalid: %v\n", err)
		os.Exit(1)
	}
	s := config.Stars

	fmt.Printf("star config v%d (updated %s)\n", s.Version, s.UpdatedAt)
	fmt.Printf("assumptions: $1 = ₹%.2f · Play fee %.0f%% · floor %.2fx · target %.2fx\n\n",
		s.Economics.USDINR, s.Economics.PlayServiceFeePct,
		s.Economics.MinMarginMultiple, s.Economics.TargetMarginMultiple)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PACK\tPRICE\tSTARS\t₹/STAR")
	for _, p := range s.SortedPacks() {
		fmt.Fprintf(w, "%s\t₹%d\t%d\t₹%.2f\n", p.ProductID, p.PriceINR, p.Stars,
			float64(p.PriceINR)/float64(p.Stars))
	}
	w.Flush()

	fmt.Printf("\nmargins are computed at the CHEAPEST star rate (₹%.2f), i.e. what a\n"+
		"customer on the biggest pack pays — the worst case for us.\n\n", s.MinStarValueINR())

	w = tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "TYPE\tQUALITY\tSTARS\tNET\tCOST\tMARGIN\tMULT\tVERDICT\t")
	failed, thin := 0, 0
	for _, m := range s.Margins() {
		verdict := "ok"
		switch {
		case m.BelowMin:
			verdict = fmt.Sprintf("FAIL — needs %d stars", m.MinStars)
			failed++
		case m.BelowTarget:
			verdict = fmt.Sprintf("thin — %d stars for target", m.TargetStars)
			thin++
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t₹%.2f\t₹%.2f\t₹%.2f\t%.2fx\t%s\n",
			m.Type, m.Quality, m.Stars, m.NetINR, m.CostINR, m.MarginINR, m.Multiple, verdict)
	}
	w.Flush()

	fmt.Printf("\nfree entitlements: %d welcome credits (%d for a returning email), "+
		"%d/day, %s quality, types %v\n",
		s.Free.WelcomeCredits, s.Free.ReturningWelcomeCredits,
		s.Free.DailyFreeCount, s.Free.FreeQuality, s.Free.FreeTypes)
	fmt.Printf("free usage is suppressed at a balance of %d stars or more (cheapest tier)\n",
		s.CheapestTierStars())

	if thin > 0 {
		fmt.Printf("\n%d tier(s) are thin: above the hard floor but below the %.2fx target.\n"+
			"Not a blocker — but they are the first to break if model costs rise.\n",
			thin, s.Economics.TargetMarginMultiple)
	}
	if failed > 0 {
		fmt.Fprintf(os.Stderr, "\n%d tier(s) are below the %.2fx hard floor and lose money once\n"+
			"refunded failures are counted. Raise the star cost, or lower\n"+
			"min_margin_multiple deliberately if you know why.\n",
			failed, s.Economics.MinMarginMultiple)
		os.Exit(1)
	}
	fmt.Println("\nall tiers clear the hard margin floor")
}
