package useragent_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/dmitrymomot/go-router/useragent"
)

func TestGeneratedPatternsWellFormed(t *testing.T) {
	mustTrue(t, len(useragent.GeneratedBotPatterns) > 0, "run go run ./useragent/gen first")
	prev := ""
	for _, p := range useragent.GeneratedBotPatterns {
		equal(t, strings.ToLower(p), p, "patterns must be lowercase")
		isTrue(t, len(p) >= 5, "pattern %q too short", p)
		isFalse(t, strings.ContainsAny(p, `\^$|?*+()[]{}`), "regex leftovers in %q", p)
		isTrue(t, prev < p, "table must be sorted and deduped")
		prev = p
	}
}

// Every generated pattern must be reachable through the runtime trigger
// gate — this catches drift between gen's trigger copy and bot.go's.
func TestGeneratedPatternsReachable(t *testing.T) {
	for _, p := range useragent.GeneratedBotPatterns {
		reachable := false
		for _, trig := range useragent.BotTriggers {
			if strings.Contains(p, trig) {
				reachable = true
				break
			}
		}
		isTrue(t, reachable, "generated pattern %q contains no bot.go trigger", p)
	}
}

// End-to-end: a UA embedding any generated pattern parses as a bot.
func TestGeneratedPatternsDetected(t *testing.T) {
	for _, p := range useragent.GeneratedBotPatterns {
		got := useragent.Parse("Mozilla/5.0 (compatible) " + p)
		isTrue(t, got.IsBot(), "pattern %q", p)
	}
}

// Curated attribution wins over the generated table.
func TestCuratedPrecedesGenerated(t *testing.T) {
	got := useragent.Parse("Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)")
	equal(t, "Googlebot", got.Bot.Name)
	equal(t, useragent.BotSearchEngine, got.Bot.Category)
}

// Spot-checks that a generated (non-curated) pattern produces a sane display
// name via botNameFromPattern. "petalbot" is present in the generated table
// and has no curated override.
func TestGeneratedPatternNaming(t *testing.T) {
	const pattern = "petalbot"
	mustTrue(t, slices.Contains(useragent.GeneratedBotPatterns, pattern), "expected %q in the generated table; pick another present, non-curated pattern if this fails", pattern)

	got := useragent.Parse("Mozilla/5.0 (compatible) " + pattern)
	equal(t, pattern, got.Bot.Name)
}
