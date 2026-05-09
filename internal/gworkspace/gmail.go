// Ported from /workspace/projects/andy/tools/gmail.go on 2026-05-09. Adapted to a CLI surface.
//
// Differences from the original:
//   - Tool registration removed; each tool's Execute body is lifted into a public function.
//   - All output formatting preserved verbatim where it appears in the originals — the CLI
//     prints these strings to stdout untouched.
//   - SEND IS NOW IMPLEMENTED. The original deliberately omits send (Andy bug history).
//     The gateway harness enforces send permissions externally; the binary supports the API.
//   - BatchModify added (used to archive by removing INBOX label) — not in the original.
//   - htmlToText was a 70-line helper in andy/tools/fetch.go. Replaced here with a tiny
//     inline regex strip — quoted-trail readability is best-effort, not load-bearing.
package gworkspace

import (
	"context"
	"encoding/base64"
	"fmt"
	"html"
	"mime"
	"net/textproto"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"google.golang.org/api/gmail/v1"
)

// ─── Public API ─────────────────────────────────────────────────────────────────

// GmailSearch searches Gmail with the given query. Returns formatted output for stdout.
func GmailSearch(ctx context.Context, gs *GoogleService, query string, maxResults int, label string) (string, error) {
	svc, err := gs.GmailService(ctx)
	if err != nil {
		return "", err
	}
	if query == "" {
		return "", fmt.Errorf("query is required")
	}
	if maxResults <= 0 {
		maxResults = 10
	}
	if maxResults > 50 {
		maxResults = 50
	}

	call := svc.Users.Messages.List("me").Q(query).MaxResults(int64(maxResults))
	if label != "" {
		call = call.LabelIds(label)
	}
	resp, err := call.Do()
	if err != nil {
		return "", fmt.Errorf("gmail search: %w", err)
	}
	if len(resp.Messages) == 0 {
		return "No messages found.", nil
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("Found %d messages:\n\n", len(resp.Messages)))

	for i, msg := range resp.Messages {
		full, err := svc.Users.Messages.Get("me", msg.Id).
			Format("metadata").
			MetadataHeaders("From", "To", "Subject", "Date").
			Do()
		if err != nil {
			b.WriteString(fmt.Sprintf("%d. [error fetching %s]\n", i+1, msg.Id))
			continue
		}

		from, to, subject, date := extractHeaders(full)
		snippet := full.Snippet
		if len(snippet) > 120 {
			snippet = snippet[:120] + "..."
		}

		unread := ""
		for _, lbl := range full.LabelIds {
			if lbl == "UNREAD" {
				unread = " [UNREAD]"
				break
			}
		}

		b.WriteString(fmt.Sprintf("%d. **%s**%s\n", i+1, subject, unread))
		b.WriteString(fmt.Sprintf("   ID: `%s` | Thread: `%s` | From: %s | To: %s\n", msg.Id, full.ThreadId, from, to))
		b.WriteString(fmt.Sprintf("   Date: %s\n", date))
		b.WriteString(fmt.Sprintf("   %s\n\n", snippet))
	}
	return b.String(), nil
}

// GmailThread fetches a thread (or single message) by ID and returns its full body.
//
// NEW: when given a thread ID we walk all messages in the thread; the original only
// supported single-message reads. Falls back to message-by-id if the thread lookup
// 404s — Gmail uses the same ID space for both in many cases.
func GmailThread(ctx context.Context, gs *GoogleService, id string) (string, error) {
	svc, err := gs.GmailService(ctx)
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", fmt.Errorf("thread/message id is required")
	}

	// Try thread first, fall back to single message.
	if th, err := svc.Users.Threads.Get("me", id).Format("full").Do(); err == nil {
		var b strings.Builder
		b.WriteString(fmt.Sprintf("Thread `%s` — %d message(s)\n\n", th.Id, len(th.Messages)))
		for i, msg := range th.Messages {
			b.WriteString(fmt.Sprintf("─── Message %d/%d ───\n", i+1, len(th.Messages)))
			b.WriteString(formatMessage(msg))
			b.WriteString("\n")
		}
		return b.String(), nil
	}

	msg, err := svc.Users.Messages.Get("me", id).Format("full").Do()
	if err != nil {
		return "", fmt.Errorf("gmail read: %w", err)
	}
	return formatMessage(msg), nil
}

// GmailLabels lists all Gmail labels.
func GmailLabels(ctx context.Context, gs *GoogleService) (string, error) {
	svc, err := gs.GmailService(ctx)
	if err != nil {
		return "", err
	}
	resp, err := svc.Users.Labels.List("me").Do()
	if err != nil {
		return "", fmt.Errorf("gmail labels: %w", err)
	}

	var b strings.Builder
	b.WriteString("Gmail Labels:\n\n")
	var system, user []string
	for _, l := range resp.Labels {
		if l.Type == "system" {
			system = append(system, l.Name)
		} else {
			user = append(user, l.Name)
		}
	}
	if len(system) > 0 {
		b.WriteString("**System:** ")
		b.WriteString(strings.Join(system, ", "))
		b.WriteString("\n\n")
	}
	if len(user) > 0 {
		b.WriteString("**User:** ")
		b.WriteString(strings.Join(user, ", "))
		b.WriteString("\n")
	}
	return b.String(), nil
}

// GmailDraftCreate saves a new draft. Mirrors andy's gmailDraftCreateTool — appends
// Steve's canonical signature and strips any model-produced sign-off first.
func GmailDraftCreate(ctx context.Context, gs *GoogleService, to, subject, body, cc string) (string, error) {
	svc, err := gs.GmailService(ctx)
	if err != nil {
		return "", err
	}
	if to == "" || subject == "" || body == "" {
		return "", fmt.Errorf("to, subject, and body are required")
	}

	bodyNoSignoff := stripModelSignoff(body)
	plainBody := bodyNoSignoff + "\r\n\r\n" + signaturePlain
	htmlBody := "<div>" + plainTextToHTML(bodyNoSignoff) + "</div><br>" + signatureHTML
	raw := buildRawMultipart(to, cc, subject, plainBody, htmlBody, "", nil)

	draft := &gmail.Draft{
		Message: &gmail.Message{
			Raw: base64.URLEncoding.EncodeToString([]byte(raw)),
		},
	}
	saved, err := svc.Users.Drafts.Create("me", draft).Do()
	if err != nil {
		return "", fmt.Errorf("gmail draft create: %w", err)
	}
	return fmt.Sprintf("Draft saved (NOT sent).\nDraft ID: %s\nMessage ID: %s", saved.Id, saved.Message.Id), nil
}

// GmailDraftReply saves a draft reply on an existing thread. The original message id
// must be a message in the thread (we use it to derive headers & ThreadId).
func GmailDraftReply(ctx context.Context, gs *GoogleService, threadOrMsgID, body string, replyAll bool) (string, error) {
	svc, err := gs.GmailService(ctx)
	if err != nil {
		return "", err
	}
	if threadOrMsgID == "" || body == "" {
		return "", fmt.Errorf("thread/message id and body are required")
	}

	// Resolve to a concrete message: if we got a thread id, use the latest message in it.
	msgID := threadOrMsgID
	threadID := ""
	if th, err := svc.Users.Threads.Get("me", threadOrMsgID).Format("full").Do(); err == nil && len(th.Messages) > 0 {
		msgID = th.Messages[len(th.Messages)-1].Id
		threadID = th.Id
	}

	orig, err := svc.Users.Messages.Get("me", msgID).Format("full").Do()
	if err != nil {
		return "", fmt.Errorf("fetch original: %w", err)
	}
	if threadID == "" {
		threadID = orig.ThreadId
	}

	origFrom, origTo, origSubject, origDate := extractHeaders(orig)
	messageID := getHeader(orig, "Message-ID")

	to := origFrom
	cc := ""
	if replyAll {
		cc = origTo
		if origCc := getHeader(orig, "Cc"); origCc != "" {
			cc += ", " + origCc
		}
	}
	subject := origSubject
	if !strings.HasPrefix(strings.ToLower(subject), "re:") {
		subject = "Re: " + subject
	}

	bodyNoSignoff := stripModelSignoff(body)
	plainBody := bodyNoSignoff + "\r\n\r\n" + signaturePlain + "\r\n\r\n" +
		formatQuotedTrail(origFrom, origDate, orig.Payload)
	htmlBody := formatReplyHTML(bodyNoSignoff, origFrom, origDate, orig.Payload)

	raw := buildRawMultipart(to, cc, subject, plainBody, htmlBody, messageID, nil)

	draft := &gmail.Draft{
		Message: &gmail.Message{
			Raw:      base64.URLEncoding.EncodeToString([]byte(raw)),
			ThreadId: threadID,
		},
	}
	saved, err := svc.Users.Drafts.Create("me", draft).Do()
	if err != nil {
		return "", fmt.Errorf("gmail draft reply: %w", err)
	}
	return fmt.Sprintf("Reply draft saved (NOT sent).\nDraft ID: %s\nThread ID: %s", saved.Id, saved.Message.ThreadId), nil
}

// GmailSend dispatches an email immediately, with optional file attachments.
//
// NEW vs the andy port: send was deliberately omitted in andy. Re-introduced here
// because the original `gog` CLI had a send command and downstream skills depend
// on it. Send permissions are gated by the gateway harness, not this binary.
func GmailSend(ctx context.Context, gs *GoogleService, to, subject, body string, attachments []string) (string, error) {
	svc, err := gs.GmailService(ctx)
	if err != nil {
		return "", err
	}
	if to == "" || subject == "" || body == "" {
		return "", fmt.Errorf("to, subject, and body are required")
	}

	bodyNoSignoff := stripModelSignoff(body)
	plainBody := bodyNoSignoff + "\r\n\r\n" + signaturePlain
	htmlBody := "<div>" + plainTextToHTML(bodyNoSignoff) + "</div><br>" + signatureHTML

	attachParts, err := loadAttachments(attachments)
	if err != nil {
		return "", err
	}
	raw := buildRawMultipart(to, "", subject, plainBody, htmlBody, "", attachParts)

	sent, err := svc.Users.Messages.Send("me", &gmail.Message{
		Raw: base64.URLEncoding.EncodeToString([]byte(raw)),
	}).Do()
	if err != nil {
		return "", fmt.Errorf("gmail send: %w", err)
	}
	return fmt.Sprintf("Sent. Message ID: %s\nThread ID: %s", sent.Id, sent.ThreadId), nil
}

// GmailBatchModify adds and/or removes labels on a list of messages.
//
// NEW: not present in andy. Common use is archiving — `--remove-label INBOX`.
func GmailBatchModify(ctx context.Context, gs *GoogleService, ids []string, addLabels, removeLabels []string) (string, error) {
	svc, err := gs.GmailService(ctx)
	if err != nil {
		return "", err
	}
	if len(ids) == 0 {
		return "", fmt.Errorf("at least one message id is required")
	}
	req := &gmail.BatchModifyMessagesRequest{
		Ids:            ids,
		AddLabelIds:    addLabels,
		RemoveLabelIds: removeLabels,
	}
	if err := svc.Users.Messages.BatchModify("me", req).Do(); err != nil {
		return "", fmt.Errorf("gmail batch modify: %w", err)
	}
	return fmt.Sprintf("Modified %d message(s). +%v -%v", len(ids), addLabels, removeLabels), nil
}

// ─── Helpers (mostly verbatim from andy) ────────────────────────────────────────

func extractHeaders(msg *gmail.Message) (from, to, subject, date string) {
	if msg.Payload == nil {
		return
	}
	for _, h := range msg.Payload.Headers {
		switch strings.ToLower(h.Name) {
		case "from":
			from = h.Value
		case "to":
			to = h.Value
		case "subject":
			subject = h.Value
		case "date":
			date = h.Value
		}
	}
	return
}

func getHeader(msg *gmail.Message, name string) string {
	if msg.Payload == nil {
		return ""
	}
	lower := strings.ToLower(name)
	for _, h := range msg.Payload.Headers {
		if strings.ToLower(h.Name) == lower {
			return h.Value
		}
	}
	return ""
}

func extractBody(part *gmail.MessagePart) string {
	if part == nil {
		return ""
	}
	if part.MimeType == "text/plain" && part.Body != nil && part.Body.Data != "" {
		if data, err := base64.URLEncoding.DecodeString(part.Body.Data); err == nil {
			return string(data)
		}
	}
	if part.MimeType == "text/html" && part.Body != nil && part.Body.Data != "" {
		if data, err := base64.URLEncoding.DecodeString(part.Body.Data); err == nil {
			return string(data)
		}
	}
	var plainText, htmlText string
	for _, sub := range part.Parts {
		result := extractBody(sub)
		if result != "" {
			if sub.MimeType == "text/plain" {
				plainText = result
			} else if sub.MimeType == "text/html" {
				htmlText = result
			} else if plainText == "" {
				plainText = result
			}
		}
	}
	if plainText != "" {
		return plainText
	}
	return htmlText
}

type gmailAttachment struct {
	name string
	mime string
	size int64
}

func extractAttachments(part *gmail.MessagePart) []gmailAttachment {
	if part == nil {
		return nil
	}
	var result []gmailAttachment
	if part.Filename != "" && part.Body != nil {
		result = append(result, gmailAttachment{
			name: part.Filename,
			mime: part.MimeType,
			size: part.Body.Size,
		})
	}
	for _, sub := range part.Parts {
		result = append(result, extractAttachments(sub)...)
	}
	return result
}

// formatMessage renders a single message with body + attachment list. Same shape
// the andy gmailReadTool produced.
func formatMessage(msg *gmail.Message) string {
	from, to, subject, date := extractHeaders(msg)
	var b strings.Builder
	b.WriteString(fmt.Sprintf("**%s**\n", subject))
	b.WriteString(fmt.Sprintf("From: %s\n", from))
	b.WriteString(fmt.Sprintf("To: %s\n", to))
	b.WriteString(fmt.Sprintf("Date: %s\n", date))
	b.WriteString(fmt.Sprintf("ID: %s | Thread: %s\n", msg.Id, msg.ThreadId))
	if len(msg.LabelIds) > 0 {
		b.WriteString(fmt.Sprintf("Labels: %s\n", strings.Join(msg.LabelIds, ", ")))
	}
	b.WriteString("\n---\n\n")

	body := extractBody(msg.Payload)
	if body == "" {
		body = msg.Snippet
	}
	if len(body) > 8000 {
		body = body[:8000] + "\n\n... [truncated at 8000 chars]"
	}
	b.WriteString(body)

	attachments := extractAttachments(msg.Payload)
	if len(attachments) > 0 {
		b.WriteString("\n\n---\n**Attachments:**\n")
		for _, a := range attachments {
			b.WriteString(fmt.Sprintf("- %s (%s, %d bytes)\n", a.name, a.mime, a.size))
		}
	}
	return b.String()
}

// ─── Quoted trail / signature (verbatim from andy) ──────────────────────────────

func formatQuotedTrail(origFrom, origDate string, payload *gmail.MessagePart) string {
	body := extractBody(payload)
	if body == "" {
		return "On " + formatAttributionDate(origDate) + ", " + origFrom + " wrote:\r\n"
	}
	if looksLikeHTML(body) {
		body = htmlToTextLite(body)
	}
	body = strings.ReplaceAll(body, "\r\n", "\n")
	body = strings.ReplaceAll(body, "\r", "\n")

	var b strings.Builder
	b.WriteString("On ")
	b.WriteString(formatAttributionDate(origDate))
	b.WriteString(", ")
	b.WriteString(origFrom)
	b.WriteString(" wrote:\r\n")
	for _, line := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
		line = strings.TrimRight(line, " \t")
		if line == "" {
			b.WriteString(">\r\n")
			continue
		}
		b.WriteString("> ")
		b.WriteString(line)
		b.WriteString("\r\n")
	}
	return b.String()
}

func formatAttributionDate(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "<unknown date>"
	}
	layouts := []string{
		time.RFC1123Z,
		time.RFC1123,
		"Mon, 2 Jan 2006 15:04:05 -0700",
		"Mon, 2 Jan 2006 15:04:05 MST",
		"2 Jan 2006 15:04:05 -0700",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.Format("Mon, 2 Jan 2006 at 15:04")
		}
	}
	return raw
}

func looksLikeHTML(s string) bool {
	lower := strings.ToLower(s)
	return strings.Contains(lower, "<html") ||
		strings.Contains(lower, "<body") ||
		strings.Contains(lower, "<div") ||
		strings.Contains(lower, "<p>") ||
		strings.Contains(lower, "<br")
}

// htmlToTextLite is a stripped-down replacement for andy's htmlToText. We don't
// need the full markdown reflow here — just enough to make the quoted-trail
// readable when the original was HTML-only.
//
// CHANGED: andy used a 70-line implementation. Here we strip tags + decode the
// most common entities and call it done. Quoted trails are user-visible context,
// not code Andy will execute, so a low-fidelity render is acceptable.
var (
	reHTMLTag    = regexp.MustCompile(`(?is)<[^>]+>`)
	reMultiSpace = regexp.MustCompile(`[ \t]+`)
	reMultiLine  = regexp.MustCompile(`\n{3,}`)
)

func htmlToTextLite(s string) string {
	s = regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</\1>`).ReplaceAllString(s, "")
	s = regexp.MustCompile(`(?i)<br[^>]*>`).ReplaceAllString(s, "\n")
	s = regexp.MustCompile(`(?i)</p>`).ReplaceAllString(s, "\n\n")
	s = regexp.MustCompile(`(?i)</div>`).ReplaceAllString(s, "\n")
	s = reHTMLTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = reMultiSpace.ReplaceAllString(s, " ")
	s = reMultiLine.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

const signaturePlain = "Kind regards,\r\n\r\n" +
	"Steve Whitehead MFPWS MPTS\r\n" +
	"T: 020 8150 3762\r\n\r\n" +
	"Grey & Associates Ltd\r\n" +
	"Dalton House, 60 Windsor Avenue London SW19 2RR\r\n" +
	"W: www.greyandassociates.co.uk"

const signatureHTML = `<div>Kind regards,<br><br>` +
	`<b>Steve Whitehead</b> MFPWS MPTS<br>` +
	`<b>T:</b> 020 8150 3762<br><br>` +
	`<b>Grey &amp; Associates Ltd</b><br>` +
	`Dalton House, 60 Windsor Avenue London SW19 2RR<br>` +
	`<b>W:</b> <a href="https://www.greyandassociates.co.uk">www.greyandassociates.co.uk</a></div>`

var signoffPrefixes = []string{
	"kind regards",
	"best regards",
	"best wishes",
	"warm regards",
	"regards",
	"yours sincerely",
	"yours faithfully",
	"many thanks",
}

func stripModelSignoff(body string) string {
	trimmed := strings.TrimRight(body, " \t\r\n")
	if trimmed == "" {
		return trimmed
	}
	lines := strings.Split(trimmed, "\n")
	start := len(lines) - 6
	if start < 0 {
		start = 0
	}
	signoffIdx := -1
	for i := start; i < len(lines); i++ {
		line := strings.ToLower(strings.TrimSpace(lines[i]))
		line = strings.TrimRight(line, ",.!?; ")
		if line == "" {
			continue
		}
		for _, p := range signoffPrefixes {
			if line == p || strings.HasPrefix(line, p+" ") {
				signoffIdx = i
				break
			}
		}
		if signoffIdx >= 0 {
			break
		}
	}
	if signoffIdx < 0 {
		return trimmed
	}
	end := signoffIdx
	for end > 0 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	return strings.TrimRight(strings.Join(lines[:end], "\n"), " \t\r\n")
}

func extractHTMLBody(part *gmail.MessagePart) string {
	if part == nil {
		return ""
	}
	if part.MimeType == "text/html" && part.Body != nil && part.Body.Data != "" {
		if data, err := base64.URLEncoding.DecodeString(part.Body.Data); err == nil {
			return string(data)
		}
	}
	for _, sub := range part.Parts {
		if s := extractHTMLBody(sub); s != "" {
			return s
		}
	}
	return ""
}

func plainTextToHTML(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = html.EscapeString(s)
	return strings.ReplaceAll(s, "\n", "<br>")
}

var (
	reDoctype       = regexp.MustCompile(`(?is)<!DOCTYPE[^>]*>`)
	reXMLDecl       = regexp.MustCompile(`(?is)<\?xml[^>]*\?>`)
	reHeadBlock     = regexp.MustCompile(`(?is)<head\b[^>]*>.*?</head>`)
	reMetaTag       = regexp.MustCompile(`(?is)<meta\b[^>]*/?>`)
	reScriptBlock   = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script>`)
	reStyleBlockTag = regexp.MustCompile(`(?is)<style\b[^>]*>.*?</style>`)
	reHTMLOrBody    = regexp.MustCompile(`(?is)</?(?:html|body)\b[^>]*>`)
)

func stripOuterHTMLWrappers(s string) string {
	s = reDoctype.ReplaceAllString(s, "")
	s = reXMLDecl.ReplaceAllString(s, "")
	s = reScriptBlock.ReplaceAllString(s, "")
	s = reStyleBlockTag.ReplaceAllString(s, "")
	s = reHeadBlock.ReplaceAllString(s, "")
	s = reMetaTag.ReplaceAllString(s, "")
	s = reHTMLOrBody.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}

func formatQuotedHTMLBlock(origFrom, origDate string, payload *gmail.MessagePart) string {
	var b strings.Builder
	b.WriteString(`<div class="gmail_quote gmail_quote_container">`)
	b.WriteString(`<div dir="ltr" class="gmail_attr">`)
	b.WriteString("On ")
	b.WriteString(html.EscapeString(formatAttributionDate(origDate)))
	b.WriteString(", ")
	b.WriteString(html.EscapeString(origFrom))
	b.WriteString(" wrote:<br></div>")
	b.WriteString(`<blockquote class="gmail_quote" style="margin:0px 0px 0px 0.8ex;border-left:1px solid rgb(204,204,204);padding-left:1ex">`)
	if htmlSrc := extractHTMLBody(payload); htmlSrc != "" {
		b.WriteString(stripOuterHTMLWrappers(htmlSrc))
	} else if plain := extractBody(payload); plain != "" {
		b.WriteString(plainTextToHTML(plain))
	}
	b.WriteString("</blockquote></div>")
	return b.String()
}

func formatReplyHTML(replyText, origFrom, origDate string, payload *gmail.MessagePart) string {
	var b strings.Builder
	b.WriteString("<div>")
	b.WriteString(plainTextToHTML(replyText))
	b.WriteString("</div><br>")
	b.WriteString(signatureHTML)
	b.WriteString("<br><br>")
	b.WriteString(formatQuotedHTMLBlock(origFrom, origDate, payload))
	return b.String()
}

// ─── Raw MIME assembly ──────────────────────────────────────────────────────────

type rawAttachment struct {
	filename string
	mime     string
	data     []byte
}

func loadAttachments(paths []string) ([]rawAttachment, error) {
	var out []rawAttachment
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("read attachment %s: %w", p, err)
		}
		mt := mime.TypeByExtension(filepath.Ext(p))
		if mt == "" {
			mt = "application/octet-stream"
		}
		out = append(out, rawAttachment{
			filename: filepath.Base(p),
			mime:     mt,
			data:     data,
		})
	}
	return out, nil
}

// buildRawMultipart writes a multipart/alternative (with optional outer
// multipart/mixed when attachments are present) RFC-5322 message.
//
// CHANGED from andy: extended to support attachments. When attachments are
// supplied we wrap the alternative part in a multipart/mixed envelope.
func buildRawMultipart(to, cc, subject, plainBody, htmlBody, inReplyTo string, attachments []rawAttachment) string {
	altBoundary := fmt.Sprintf("----=_GogAlt_%d", time.Now().UnixNano())
	mixedBoundary := fmt.Sprintf("----=_GogMixed_%d", time.Now().UnixNano())

	// Headers
	hdr := textproto.MIMEHeader{}
	hdr.Set("To", to)
	if cc != "" {
		hdr.Set("Cc", cc)
	}
	hdr.Set("Subject", subject)
	if inReplyTo != "" {
		hdr.Set("In-Reply-To", inReplyTo)
		hdr.Set("References", inReplyTo)
	}
	hdr.Set("MIME-Version", "1.0")

	hasAttach := len(attachments) > 0

	var b strings.Builder
	for k, vs := range hdr {
		for _, v := range vs {
			b.WriteString(k + ": " + v + "\r\n")
		}
	}

	if hasAttach {
		b.WriteString(`Content-Type: multipart/mixed; boundary="` + mixedBoundary + `"` + "\r\n\r\n")
		b.WriteString("--" + mixedBoundary + "\r\n")
	}

	// Alternative wrapper
	b.WriteString(`Content-Type: multipart/alternative; boundary="` + altBoundary + `"` + "\r\n\r\n")

	b.WriteString("--" + altBoundary + "\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
	b.WriteString(wrapBase64Lines(base64.StdEncoding.EncodeToString([]byte(plainBody))))
	b.WriteString("\r\n")

	b.WriteString("--" + altBoundary + "\r\n")
	b.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
	b.WriteString(wrapBase64Lines(base64.StdEncoding.EncodeToString([]byte(htmlBody))))
	b.WriteString("\r\n")

	b.WriteString("--" + altBoundary + "--\r\n")

	if hasAttach {
		for _, a := range attachments {
			b.WriteString("\r\n--" + mixedBoundary + "\r\n")
			b.WriteString("Content-Type: " + a.mime + `; name="` + a.filename + `"` + "\r\n")
			b.WriteString(`Content-Disposition: attachment; filename="` + a.filename + `"` + "\r\n")
			b.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
			b.WriteString(wrapBase64Lines(base64.StdEncoding.EncodeToString(a.data)))
			b.WriteString("\r\n")
		}
		b.WriteString("--" + mixedBoundary + "--\r\n")
	}

	return b.String()
}

func wrapBase64Lines(s string) string {
	const width = 76
	if len(s) <= width {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i += width {
		end := i + width
		if end > len(s) {
			end = len(s)
		}
		b.WriteString(s[i:end])
		if end < len(s) {
			b.WriteString("\r\n")
		}
	}
	return b.String()
}
