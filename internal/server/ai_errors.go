package server

import (
	"errors"

	"cadence-cards/internal/claude"
)

// aiErrorMessage maps an AI failure to user-facing bubble copy. The states are
// deliberately distinct so the user knows whether to retry now, wait, or tell
// the operator the key is broken. Studying itself never depends on these —
// the card and grading always render regardless of AI availability.
func aiErrorMessage(err error) string {
	switch {
	case errors.Is(err, claude.ErrRateLimited):
		return "Claude is rate-limited right now. Wait a minute and try again."
	case errors.Is(err, claude.ErrOverloaded):
		return "Anthropic's API is overloaded at the moment. Try again shortly."
	case errors.Is(err, claude.ErrNotConfigured):
		return "This server has no Claude API key, so AI features are switched off. Set CLAUDE_API_KEY and restart to turn them on."
	case errors.Is(err, claude.ErrBadAuth):
		return "The server's Claude API key was rejected — check the key and account credit."
	case errors.Is(err, claude.ErrRefused):
		return "Claude declined this request. Try rephrasing your message."
	default:
		return "Sorry, something went wrong while contacting Claude. Please try again."
	}
}
