// Ported from /workspace/projects/andy/tools/google_auth.go on 2026-05-09. Adapted to a CLI surface.
//
// Differences from the original:
//   - Package renamed from `tools` to `gworkspace`.
//   - Default token path is /root/.config/gog/token.json (was google_token.json) — this is
//     the writable volume in the claude-gateway container.
//   - Status message references `gog auth` instead of `/googleauth`.
//   - AuthCodeURL state changed from "andy-auth" to "gog-auth".
package gworkspace

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/calendar/v3"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
	"google.golang.org/api/sheets/v4"
)

// GoogleService manages OAuth2 credentials and provides authenticated API clients.
type GoogleService struct {
	config    *oauth2.Config
	tokenPath string

	mu    sync.RWMutex
	token *oauth2.Token
}

// GoogleConfig holds the configuration needed to create a GoogleService.
type GoogleConfig struct {
	ClientID     string
	ClientSecret string
	TokenPath    string // path to token.json
	RedirectURL  string // OAuth callback URL (must match Google Console)
}

// NewGoogleService creates a new Google API service.
// It loads the token from disk if available. If no token exists,
// you must complete the OAuth flow via AuthURL + ExchangeCode.
func NewGoogleService(cfg GoogleConfig) (*GoogleService, error) {
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, fmt.Errorf("GOOGLE_CLIENT_ID and GOOGLE_CLIENT_SECRET are required")
	}
	if cfg.TokenPath == "" {
		// CHANGED: default path matches the claude-gateway writable volume layout.
		cfg.TokenPath = "/root/.config/gog/token.json"
	}
	if cfg.RedirectURL == "" {
		// Use localhost redirect — Google will redirect the browser there.
		// The page won't load (no server), but the code is in the URL bar.
		// User copies the code param and sends it via `gog auth exchange <code>`.
		cfg.RedirectURL = "http://localhost:5000/oauth2callback"
	}

	oauthConfig := &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		RedirectURL:  cfg.RedirectURL,
		Scopes: []string{
			gmail.GmailReadonlyScope,
			gmail.GmailModifyScope,
			gmail.GmailSendScope,
			calendar.CalendarReadonlyScope,
			calendar.CalendarEventsScope,
			drive.DriveScope,
			sheets.SpreadsheetsScope,
		},
		Endpoint: google.Endpoint,
	}

	gs := &GoogleService{
		config:    oauthConfig,
		tokenPath: cfg.TokenPath,
	}

	// Try loading existing token
	if err := gs.loadToken(); err != nil {
		log.Printf("gog: no valid token at %s (run `gog auth url` to authenticate)", cfg.TokenPath)
	} else {
		log.Printf("gog: token loaded from %s", cfg.TokenPath)
	}

	return gs, nil
}

// IsAuthenticated returns true if we have a valid (or refreshable) token.
func (gs *GoogleService) IsAuthenticated() bool {
	gs.mu.RLock()
	defer gs.mu.RUnlock()
	return gs.token != nil && (gs.token.Valid() || gs.token.RefreshToken != "")
}

// AuthURL returns the URL the user must visit to authorize the gateway.
func (gs *GoogleService) AuthURL() string {
	// CHANGED: state token now "gog-auth" rather than "andy-auth".
	return gs.config.AuthCodeURL("gog-auth", oauth2.AccessTypeOffline, oauth2.ApprovalForce)
}

// ExchangeCode exchanges an authorization code for a token and saves it.
func (gs *GoogleService) ExchangeCode(ctx context.Context, code string) error {
	token, err := gs.config.Exchange(ctx, code)
	if err != nil {
		return fmt.Errorf("token exchange: %w", err)
	}

	gs.mu.Lock()
	gs.token = token
	gs.mu.Unlock()

	return gs.saveToken()
}

// httpClient returns an authenticated HTTP client, refreshing the token if needed.
func (gs *GoogleService) httpClient(ctx context.Context) (*http.Client, error) {
	gs.mu.RLock()
	tok := gs.token
	gs.mu.RUnlock()

	if tok == nil {
		return nil, fmt.Errorf("not authenticated — use `gog auth url` to get the OAuth URL")
	}

	// Let oauth2 handle refresh automatically
	src := gs.config.TokenSource(ctx, tok)
	newTok, err := src.Token()
	if err != nil {
		return nil, fmt.Errorf("token refresh: %w", err)
	}

	// If the token was refreshed, save it
	if newTok.AccessToken != tok.AccessToken {
		gs.mu.Lock()
		gs.token = newTok
		gs.mu.Unlock()
		if err := gs.saveToken(); err != nil {
			log.Printf("gog: failed to save refreshed token: %v", err)
		}
	}

	return oauth2.NewClient(ctx, src), nil
}

// GmailService returns an authenticated Gmail API service.
func (gs *GoogleService) GmailService(ctx context.Context) (*gmail.Service, error) {
	client, err := gs.httpClient(ctx)
	if err != nil {
		return nil, err
	}
	return gmail.NewService(ctx, option.WithHTTPClient(client))
}

// CalendarService returns an authenticated Google Calendar API service.
func (gs *GoogleService) CalendarService(ctx context.Context) (*calendar.Service, error) {
	client, err := gs.httpClient(ctx)
	if err != nil {
		return nil, err
	}
	return calendar.NewService(ctx, option.WithHTTPClient(client))
}

// DriveService returns an authenticated Google Drive API service.
func (gs *GoogleService) DriveService(ctx context.Context) (*drive.Service, error) {
	client, err := gs.httpClient(ctx)
	if err != nil {
		return nil, err
	}
	return drive.NewService(ctx, option.WithHTTPClient(client))
}

// SheetsService returns an authenticated Google Sheets API service.
func (gs *GoogleService) SheetsService(ctx context.Context) (*sheets.Service, error) {
	client, err := gs.httpClient(ctx)
	if err != nil {
		return nil, err
	}
	return sheets.NewService(ctx, option.WithHTTPClient(client))
}

// loadToken reads the token from the file.
func (gs *GoogleService) loadToken() error {
	data, err := os.ReadFile(gs.tokenPath)
	if err != nil {
		return err
	}

	var tok oauth2.Token
	if err := json.Unmarshal(data, &tok); err != nil {
		return fmt.Errorf("parse token: %w", err)
	}

	gs.mu.Lock()
	gs.token = &tok
	gs.mu.Unlock()
	return nil
}

// saveToken writes the token to disk.
//
// NEW: ensures the parent directory exists first. The original assumed the
// directory was pre-created; in the gateway container the volume mount may
// be empty on first boot.
func (gs *GoogleService) saveToken() error {
	gs.mu.RLock()
	tok := gs.token
	gs.mu.RUnlock()

	data, err := json.MarshalIndent(tok, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal token: %w", err)
	}

	if dir := filepath.Dir(gs.tokenPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return fmt.Errorf("mkdir token dir: %w", err)
		}
	}

	// Write atomically: write to tmp then rename, so a crash mid-write can't
	// leave a half-formed token file. Critical for refresh flows.
	tmp := gs.tokenPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("write token: %w", err)
	}
	if err := os.Rename(tmp, gs.tokenPath); err != nil {
		return fmt.Errorf("rename token: %w", err)
	}
	return nil
}

// LoadClientCredentialsJSON parses the standard Google "installed app" or
// "web app" client_secret JSON format and returns (clientID, clientSecret).
// Used so the gateway and gog CLI can fall back to a mounted JSON file when
// GOOGLE_CLIENT_ID/SECRET env vars aren't set, avoiding having to copy values
// out of the JSON manually.
func LoadClientCredentialsJSON(path string) (clientID, clientSecret string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	var raw map[string]map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return "", "", fmt.Errorf("parse client secret JSON: %w", err)
	}
	for _, key := range []string{"installed", "web"} {
		if section, ok := raw[key]; ok {
			id, _ := section["client_id"].(string)
			secret, _ := section["client_secret"].(string)
			if id != "" && secret != "" {
				return id, secret, nil
			}
		}
	}
	return "", "", fmt.Errorf("client_id/client_secret not found in %s (expected 'installed' or 'web' section)", path)
}

// EnsureTokenDir creates the parent directory of the token path so the first
// saveToken() call doesn't fail on a fresh container with no /root/.config/gog.
// Idempotent.
func EnsureTokenDir(tokenPath string) error {
	dir := filepath.Dir(tokenPath)
	return os.MkdirAll(dir, 0700)
}

// Status returns a human-readable status string.
func (gs *GoogleService) Status() string {
	gs.mu.RLock()
	defer gs.mu.RUnlock()

	if gs.token == nil {
		return "Not authenticated. Run `gog auth url` to start the OAuth flow."
	}
	if gs.token.Valid() {
		return fmt.Sprintf("Authenticated. Token expires: %s", gs.token.Expiry.Format(time.RFC3339))
	}
	if gs.token.RefreshToken != "" {
		return "Token expired but refreshable (will auto-refresh on next API call)."
	}
	return "Token expired and no refresh token. Re-authenticate via `gog auth url`."
}
