// /grokauth — SuperGrok / X Premium+ device-code login for Grok Build.
//
// SuperGrok is a subscription, not an API key. The sidecar runs
// `grok login --device-auth`, we surface the URL + user code here, and
// poll until the CLI writes ~/.grok/auth.json.
//
//	/grokauth          — start device login (or show status if already in)
//	/grokauth status   — show binary + login state
package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type grokStatusResp struct {
	Binary   bool `json:"binary"`
	LoggedIn bool `json:"logged_in"`
}

type grokLoginResp struct {
	Started  bool   `json:"started"`
	Already  bool   `json:"already"`
	URL      string `json:"url"`
	Code     string `json:"code"`
	Done     bool   `json:"done"`
	OK       bool   `json:"ok"`
	Error    string `json:"error"`
	LoggedIn bool   `json:"logged_in"`
}

func (b *Bot) handleGrokAuth(ctx context.Context, msg *tgbotapi.Message) {
	base := strings.TrimRight(b.cfg.Sidecar.URL, "/")
	if base == "" {
		b.sendMessage(msg.Chat.ID, "Sidecar URL is not configured.")
		return
	}

	arg := strings.TrimSpace(msg.CommandArguments())
	if arg == "status" {
		st, err := getGrokStatus(ctx, base)
		if err != nil {
			b.sendMessage(msg.Chat.ID, fmt.Sprintf("Could not reach sidecar: %v", err))
			return
		}
		b.sendMessage(msg.Chat.ID, formatGrokStatus(st))
		return
	}

	st, err := getGrokStatus(ctx, base)
	if err != nil {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Could not reach sidecar: %v", err))
		return
	}
	if !st.Binary {
		b.sendMessage(msg.Chat.ID, "Grok Build CLI is not installed in the sidecar. Rebuild the sidecar image.")
		return
	}
	if st.LoggedIn {
		b.sendMessage(msg.Chat.ID, "Already signed in to SuperGrok.\n\nSwitch with /framework grok or /models.")
		return
	}

	waitCtx, waitCancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer waitCancel()

	if _, err := postGrokLogin(waitCtx, base); err != nil {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Failed to start login: %v", err))
		return
	}

	b.sendMessage(msg.Chat.ID, "Starting SuperGrok device login…\nOpen the URL it prints, enter the code, and approve access. I'll wait here.")

	deadline := time.Now().Add(5 * time.Minute)
	sentPrompt := false
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-waitCtx.Done():
			b.sendMessage(msg.Chat.ID, "Login timed out. Send /grokauth to start again.")
			return
		case <-ticker.C:
			poll, pErr := getGrokLogin(waitCtx, base)
			if pErr != nil {
				b.sendMessage(msg.Chat.ID, fmt.Sprintf("Login poll failed: %v", pErr))
				return
			}
			if poll.LoggedIn || (poll.Done && poll.OK) {
				b.sendMessage(msg.Chat.ID, "SuperGrok signed in. Use /framework grok or /models → Grok 4.5.\nThis uses your subscription, not the API key.")
				return
			}
			if poll.Done && !poll.OK {
				errText := poll.Error
				if errText == "" {
					errText = "login did not complete"
				}
				b.sendMessage(msg.Chat.ID, "SuperGrok login failed: "+errText+"\nSend /grokauth to try again.")
				return
			}
			if !sentPrompt && (poll.URL != "" || poll.Code != "") {
				var sb strings.Builder
				sb.WriteString("Approve SuperGrok access:\n")
				if poll.URL != "" {
					sb.WriteString("\n")
					sb.WriteString(poll.URL)
					sb.WriteString("\n")
				}
				if poll.Code != "" {
					sb.WriteString("\nCode: `")
					sb.WriteString(poll.Code)
					sb.WriteString("`")
				}
				sb.WriteString("\n\nWaiting for you to finish in the browser…")
				b.sendMessage(msg.Chat.ID, sb.String())
				sentPrompt = true
			}
			if time.Now().After(deadline) {
				b.sendMessage(msg.Chat.ID, "Login timed out (5 minutes). Send /grokauth to start again.")
				return
			}
		}
	}
}

func formatGrokStatus(st grokStatusResp) string {
	bin := "missing"
	if st.Binary {
		bin = "installed"
	}
	auth := "not signed in — send /grokauth"
	if st.LoggedIn {
		auth = "signed in (SuperGrok subscription)"
	}
	return fmt.Sprintf("Grok Build CLI: %s\nSuperGrok: %s", bin, auth)
}

func getGrokStatus(ctx context.Context, base string) (grokStatusResp, error) {
	var out grokStatusResp
	if err := grokHTTP(ctx, http.MethodGet, base+"/grok/status", &out); err != nil {
		return out, err
	}
	return out, nil
}

func postGrokLogin(ctx context.Context, base string) (grokLoginResp, error) {
	var out grokLoginResp
	if err := grokHTTP(ctx, http.MethodPost, base+"/grok/login", &out); err != nil {
		return out, err
	}
	return out, nil
}

func getGrokLogin(ctx context.Context, base string) (grokLoginResp, error) {
	var out grokLoginResp
	if err := grokHTTP(ctx, http.MethodGet, base+"/grok/login", &out); err != nil {
		return out, err
	}
	return out, nil
}

func grokHTTP(ctx context.Context, method, url string, dest any) error {
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, method, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("sidecar HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if dest == nil {
		return nil
	}
	return json.Unmarshal(body, dest)
}
