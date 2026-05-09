// /googleauth — initiate or complete the Google Workspace OAuth flow.
//
// Replaces the previous "gog CLI keyring" model. The new flow uses a token
// JSON file on a Docker volume mounted at /root/.config/gog/, shared between
// the gateway (this code) and the sidecar's `gog` binary. Refreshes happen
// in-process and are atomically saved, so restarts no longer wipe the token.
//
// Usage from Telegram:
//
//	/googleauth                 → print the OAuth URL
//	/googleauth status          → show token status
//	/googleauth <code>          → exchange the auth code for a token
package bot

import (
	"context"
	"fmt"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func (b *Bot) handleGoogleAuth(ctx context.Context, msg *tgbotapi.Message) {
	if b.google == nil {
		b.sendMessage(msg.Chat.ID, "Google Workspace integration not configured. Set GOOGLE_CLIENT_ID and GOOGLE_CLIENT_SECRET (or mount client_secret.json) and restart the gateway.")
		return
	}

	arg := strings.TrimSpace(msg.CommandArguments())

	switch {
	case arg == "" || arg == "url":
		url := b.google.AuthURL()
		text := "Open this URL in your browser, authorise the app, and copy the `code` query parameter from the redirect URL:\n\n" +
			url + "\n\n" +
			"Then send: `/googleauth <paste-code-here>`"
		b.sendMessage(msg.Chat.ID, text)

	case arg == "status":
		b.sendMessage(msg.Chat.ID, b.google.Status())

	default:
		// Treat any other arg as the auth code. ExchangeCode wraps Google's
		// /token endpoint and writes the token JSON to the shared volume.
		exchangeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()

		if err := b.google.ExchangeCode(exchangeCtx, arg); err != nil {
			b.sendMessage(msg.Chat.ID, fmt.Sprintf("Auth failed: %v", err))
			return
		}
		b.sendMessage(msg.Chat.ID, "Authenticated. Token saved — gog commands and watchers will refresh automatically from now on.\n\n"+b.google.Status())
	}
}
