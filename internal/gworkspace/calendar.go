// Ported from /workspace/projects/andy/tools/gcal.go on 2026-05-09. Adapted to a CLI surface.
//
// Differences from the original:
//   - Tool wrappers stripped; logic is exposed as plain functions.
//   - CalendarList accepts an explicit time range (from/to/days) instead of always
//     "now → now+days". Brief asks for --from/--to flags.
//   - CalendarConflicts is new: not in the original. Detects overlapping events.
//   - CalendarUpdate accepts the same flag set as Create — original had a
//     subset (no attendees update). Extended for parity with the CLI surface.
package gworkspace

import (
	"context"
	"fmt"
	"strings"
	"time"

	"google.golang.org/api/calendar/v3"
)

// CalendarList lists events in [from, to). If from is zero, defaults to now.
// If to is zero, defaults to from + days. If days is 0, defaults to 7.
func CalendarList(ctx context.Context, gs *GoogleService, calID, query string, from, to time.Time, days, maxResults int) (string, error) {
	svc, err := gs.CalendarService(ctx)
	if err != nil {
		return "", err
	}
	if calID == "" {
		calID = "primary"
	}
	if days <= 0 {
		days = 7
	}
	if days > 90 {
		days = 90
	}
	if maxResults <= 0 {
		maxResults = 20
	}
	if maxResults > 100 {
		maxResults = 100
	}
	if from.IsZero() {
		from = time.Now()
	}
	if to.IsZero() {
		to = from.AddDate(0, 0, days)
	}

	call := svc.Events.List(calID).
		TimeMin(from.Format(time.RFC3339)).
		TimeMax(to.Format(time.RFC3339)).
		MaxResults(int64(maxResults)).
		SingleEvents(true).
		OrderBy("startTime")
	if query != "" {
		call = call.Q(query)
	}

	events, err := call.Do()
	if err != nil {
		return "", fmt.Errorf("gcal list: %w", err)
	}
	if len(events.Items) == 0 {
		return fmt.Sprintf("No events found between %s and %s.", from.Format("2006-01-02"), to.Format("2006-01-02")), nil
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("**%d events** between %s and %s:\n\n",
		len(events.Items), from.Format("2006-01-02"), to.Format("2006-01-02")))
	for i, ev := range events.Items {
		summary := ev.Summary
		if summary == "" {
			summary = "(no title)"
		}
		b.WriteString(fmt.Sprintf("%d. **%s**\n", i+1, summary))
		b.WriteString(fmt.Sprintf("   ID: `%s`\n", ev.Id))
		b.WriteString(fmt.Sprintf("   When: %s → %s\n", formatEventTime(ev.Start), formatEventTime(ev.End)))
		if ev.Location != "" {
			b.WriteString(fmt.Sprintf("   Location: %s\n", ev.Location))
		}
		if ev.Status == "cancelled" {
			b.WriteString("   Status: CANCELLED\n")
		}
		if len(ev.Attendees) > 0 {
			var names []string
			for _, a := range ev.Attendees {
				name := a.DisplayName
				if name == "" {
					name = a.Email
				}
				if a.ResponseStatus == "declined" {
					name += " (declined)"
				}
				names = append(names, name)
			}
			b.WriteString(fmt.Sprintf("   Attendees: %s\n", strings.Join(names, ", ")))
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

// CalendarGet fetches one event by ID.
func CalendarGet(ctx context.Context, gs *GoogleService, calID, id string) (string, error) {
	svc, err := gs.CalendarService(ctx)
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", fmt.Errorf("event id is required")
	}
	if calID == "" {
		calID = "primary"
	}
	ev, err := svc.Events.Get(calID, id).Do()
	if err != nil {
		return "", fmt.Errorf("gcal get: %w", err)
	}
	return formatEventFull(ev), nil
}

// CalendarCreateParams collects optional fields. Either End or Duration may be set;
// if both are zero, defaults to a 1-hour event.
type CalendarCreateParams struct {
	CalendarID  string
	Summary     string
	Start       string // RFC3339 or "YYYY-MM-DD" for all-day
	End         string // RFC3339 or "YYYY-MM-DD"
	Duration    time.Duration
	Description string
	Location    string
	Attendees   []string
}

// CalendarCreate creates an event. The brief calls this `gog calendar add`.
func CalendarCreate(ctx context.Context, gs *GoogleService, p CalendarCreateParams) (string, error) {
	svc, err := gs.CalendarService(ctx)
	if err != nil {
		return "", err
	}
	if p.Summary == "" || p.Start == "" {
		return "", fmt.Errorf("title (summary) and start are required")
	}
	calID := p.CalendarID
	if calID == "" {
		calID = "primary"
	}

	ev := &calendar.Event{
		Summary:     p.Summary,
		Description: p.Description,
		Location:    p.Location,
	}
	startEnd, err := buildStartEnd(p.Start, p.End, p.Duration)
	if err != nil {
		return "", err
	}
	ev.Start, ev.End = startEnd[0], startEnd[1]

	for _, email := range p.Attendees {
		email = strings.TrimSpace(email)
		if email != "" {
			ev.Attendees = append(ev.Attendees, &calendar.EventAttendee{Email: email})
		}
	}

	created, err := svc.Events.Insert(calID, ev).Do()
	if err != nil {
		return "", fmt.Errorf("gcal create: %w", err)
	}
	return fmt.Sprintf("Event created.\nID: %s\nLink: %s\n\n%s", created.Id, created.HtmlLink, formatEventFull(created)), nil
}

// CalendarUpdate updates an event. Empty fields are left unchanged.
func CalendarUpdate(ctx context.Context, gs *GoogleService, eventID string, p CalendarCreateParams) (string, error) {
	svc, err := gs.CalendarService(ctx)
	if err != nil {
		return "", err
	}
	if eventID == "" {
		return "", fmt.Errorf("event id is required")
	}
	calID := p.CalendarID
	if calID == "" {
		calID = "primary"
	}
	ev, err := svc.Events.Get(calID, eventID).Do()
	if err != nil {
		return "", fmt.Errorf("gcal get for update: %w", err)
	}
	if p.Summary != "" {
		ev.Summary = p.Summary
	}
	if p.Description != "" {
		ev.Description = p.Description
	}
	if p.Location != "" {
		ev.Location = p.Location
	}
	if p.Start != "" {
		startEnd, err := buildStartEnd(p.Start, p.End, p.Duration)
		if err != nil {
			return "", err
		}
		ev.Start, ev.End = startEnd[0], startEnd[1]
	} else if p.End != "" {
		// End-only update.
		if len(p.End) == 10 {
			ev.End = &calendar.EventDateTime{Date: p.End}
		} else {
			ev.End = &calendar.EventDateTime{DateTime: p.End}
		}
	}
	if len(p.Attendees) > 0 {
		ev.Attendees = nil
		for _, email := range p.Attendees {
			email = strings.TrimSpace(email)
			if email != "" {
				ev.Attendees = append(ev.Attendees, &calendar.EventAttendee{Email: email})
			}
		}
	}
	updated, err := svc.Events.Update(calID, eventID, ev).Do()
	if err != nil {
		return "", fmt.Errorf("gcal update: %w", err)
	}
	return fmt.Sprintf("Event updated.\n\n%s", formatEventFull(updated)), nil
}

// CalendarDelete deletes an event.
func CalendarDelete(ctx context.Context, gs *GoogleService, calID, eventID string) (string, error) {
	svc, err := gs.CalendarService(ctx)
	if err != nil {
		return "", err
	}
	if eventID == "" {
		return "", fmt.Errorf("event id is required")
	}
	if calID == "" {
		calID = "primary"
	}
	if err := svc.Events.Delete(calID, eventID).Do(); err != nil {
		return "", fmt.Errorf("gcal delete: %w", err)
	}
	return fmt.Sprintf("Event %s deleted.", eventID), nil
}

// CalendarCalendars lists all calendars on the account.
func CalendarCalendars(ctx context.Context, gs *GoogleService) (string, error) {
	svc, err := gs.CalendarService(ctx)
	if err != nil {
		return "", err
	}
	list, err := svc.CalendarList.List().Do()
	if err != nil {
		return "", fmt.Errorf("gcal calendars: %w", err)
	}
	var b strings.Builder
	b.WriteString("**Google Calendars:**\n\n")
	for i, cal := range list.Items {
		primary := ""
		if cal.Primary {
			primary = " *(primary)*"
		}
		b.WriteString(fmt.Sprintf("%d. **%s**%s\n", i+1, cal.Summary, primary))
		b.WriteString(fmt.Sprintf("   ID: `%s`\n", cal.Id))
		if cal.Description != "" {
			b.WriteString(fmt.Sprintf("   %s\n", cal.Description))
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

// CalendarConflicts reports overlapping events in [from, to). NEW — not in the andy
// source. O(n^2) sweep; fine for the small windows we look at.
func CalendarConflicts(ctx context.Context, gs *GoogleService, calID string, from, to time.Time) (string, error) {
	svc, err := gs.CalendarService(ctx)
	if err != nil {
		return "", err
	}
	if calID == "" {
		calID = "primary"
	}
	if from.IsZero() {
		from = time.Now()
	}
	if to.IsZero() {
		to = from.AddDate(0, 0, 7)
	}
	resp, err := svc.Events.List(calID).
		TimeMin(from.Format(time.RFC3339)).
		TimeMax(to.Format(time.RFC3339)).
		SingleEvents(true).
		OrderBy("startTime").
		MaxResults(250).
		Do()
	if err != nil {
		return "", fmt.Errorf("gcal conflicts: %w", err)
	}

	type interval struct {
		ev    *calendar.Event
		start time.Time
		end   time.Time
	}
	var ivs []interval
	for _, ev := range resp.Items {
		if ev.Status == "cancelled" {
			continue
		}
		s := parseEventInstant(ev.Start)
		e := parseEventInstant(ev.End)
		if s.IsZero() || e.IsZero() {
			continue
		}
		ivs = append(ivs, interval{ev, s, e})
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("Conflict scan %s → %s (%d events):\n\n",
		from.Format("2006-01-02"), to.Format("2006-01-02"), len(ivs)))
	clashes := 0
	for i := 0; i < len(ivs); i++ {
		for j := i + 1; j < len(ivs); j++ {
			a, c := ivs[i], ivs[j]
			if a.start.Before(c.end) && c.start.Before(a.end) {
				clashes++
				b.WriteString(fmt.Sprintf("Conflict %d:\n", clashes))
				b.WriteString(fmt.Sprintf("  - %s (%s → %s) [%s]\n", safeSummary(a.ev), a.start.Format("Mon 02 Jan 15:04"), a.end.Format("15:04"), a.ev.Id))
				b.WriteString(fmt.Sprintf("  - %s (%s → %s) [%s]\n\n", safeSummary(c.ev), c.start.Format("Mon 02 Jan 15:04"), c.end.Format("15:04"), c.ev.Id))
			}
		}
	}
	if clashes == 0 {
		b.WriteString("No conflicts found.\n")
	}
	return b.String(), nil
}

// ─── Helpers ───────────────────────────────────────────────────────────────────

func safeSummary(ev *calendar.Event) string {
	if ev == nil || ev.Summary == "" {
		return "(no title)"
	}
	return ev.Summary
}

func parseEventInstant(dt *calendar.EventDateTime) time.Time {
	if dt == nil {
		return time.Time{}
	}
	if dt.DateTime != "" {
		if t, err := time.Parse(time.RFC3339, dt.DateTime); err == nil {
			return t
		}
	}
	if dt.Date != "" {
		if t, err := time.Parse("2006-01-02", dt.Date); err == nil {
			return t
		}
	}
	return time.Time{}
}

// buildStartEnd resolves a start string + optional end/duration into the two
// *calendar.EventDateTime fields. Mirrors andy's logic but lifted into a helper
// so create/update share it.
func buildStartEnd(startStr, endStr string, dur time.Duration) ([2]*calendar.EventDateTime, error) {
	var out [2]*calendar.EventDateTime
	isAllDay := len(startStr) == 10
	if isAllDay {
		out[0] = &calendar.EventDateTime{Date: startStr}
		if endStr == "" {
			t, err := time.Parse("2006-01-02", startStr)
			if err != nil {
				return out, fmt.Errorf("start date parse: %w", err)
			}
			endStr = t.AddDate(0, 0, 1).Format("2006-01-02")
		}
		out[1] = &calendar.EventDateTime{Date: endStr}
		return out, nil
	}

	out[0] = &calendar.EventDateTime{DateTime: startStr}
	if endStr == "" {
		t, err := time.Parse(time.RFC3339, startStr)
		if err != nil {
			return out, fmt.Errorf("start must be RFC3339 (e.g. 2025-03-15T10:00:00Z) or date (2025-03-15)")
		}
		if dur <= 0 {
			dur = time.Hour
		}
		endStr = t.Add(dur).Format(time.RFC3339)
	}
	out[1] = &calendar.EventDateTime{DateTime: endStr}
	return out, nil
}

func formatEventTime(dt *calendar.EventDateTime) string {
	if dt == nil {
		return "?"
	}
	if dt.Date != "" {
		return dt.Date + " (all day)"
	}
	t, err := time.Parse(time.RFC3339, dt.DateTime)
	if err != nil {
		return dt.DateTime
	}
	return t.Local().Format("Mon 02 Jan 15:04")
}

func formatEventFull(ev *calendar.Event) string {
	var b strings.Builder
	summary := ev.Summary
	if summary == "" {
		summary = "(no title)"
	}
	b.WriteString(fmt.Sprintf("**%s**\n", summary))
	b.WriteString(fmt.Sprintf("ID: `%s`\n", ev.Id))
	b.WriteString(fmt.Sprintf("Status: %s\n", ev.Status))
	b.WriteString(fmt.Sprintf("When: %s → %s\n", formatEventTime(ev.Start), formatEventTime(ev.End)))
	if ev.Location != "" {
		b.WriteString(fmt.Sprintf("Location: %s\n", ev.Location))
	}
	if ev.Description != "" {
		desc := ev.Description
		if len(desc) > 500 {
			desc = desc[:500] + "..."
		}
		b.WriteString(fmt.Sprintf("Description: %s\n", desc))
	}
	if ev.HtmlLink != "" {
		b.WriteString(fmt.Sprintf("Link: %s\n", ev.HtmlLink))
	}
	if ev.Creator != nil {
		b.WriteString(fmt.Sprintf("Creator: %s\n", ev.Creator.Email))
	}
	if len(ev.Attendees) > 0 {
		b.WriteString("Attendees:\n")
		for _, a := range ev.Attendees {
			name := a.DisplayName
			if name == "" {
				name = a.Email
			}
			b.WriteString(fmt.Sprintf("  - %s (%s)\n", name, a.ResponseStatus))
		}
	}
	if ev.Recurrence != nil {
		b.WriteString(fmt.Sprintf("Recurrence: %s\n", strings.Join(ev.Recurrence, "; ")))
	}
	return b.String()
}
