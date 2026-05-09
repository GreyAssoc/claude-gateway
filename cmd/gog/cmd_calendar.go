// calendar subcommand surface.
package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/you/claude-gateway/internal/gworkspace"
)

const calUsage = `Usage:
  gog calendar list [--from DATE] [--to DATE] [--days N] [--max N] [--query Q] [--calendar ID]
  gog calendar add --title <s> --start "<datetime>" --duration <d> [--end <dt>] [--description <s>] [--attendees a,b,c] [--calendar ID]
  gog calendar update <event_id> [same flags as add]
  gog calendar delete <event_id> [--calendar ID]
  gog calendar conflicts --from <date> --to <date> [--calendar ID]
  gog calendar calendars
`

func runCalendar(ctx context.Context, gs *gworkspace.GoogleService, args []string) error {
	if len(args) < 1 {
		fmt.Print(calUsage)
		return nil
	}
	switch args[0] {
	case "list":
		return calList(ctx, gs, args[1:])
	case "add", "create":
		return calAdd(ctx, gs, args[1:])
	case "update":
		return calUpdate(ctx, gs, args[1:])
	case "delete":
		return calDelete(ctx, gs, args[1:])
	case "conflicts":
		return calConflicts(ctx, gs, args[1:])
	case "calendars":
		return calCalendars(ctx, gs)
	default:
		fmt.Print(calUsage)
		return fmt.Errorf("unknown calendar subcommand %q", args[0])
	}
}

func calList(ctx context.Context, gs *gworkspace.GoogleService, args []string) error {
	fs := newFlagSet()
	from := fs.String("from", "")
	to := fs.String("to", "")
	days := fs.Int("days", 0)
	max := fs.Int("max", 20)
	query := fs.String("query", "")
	calID := fs.String("calendar", "")
	if _, err := fs.Parse(args); err != nil {
		return err
	}
	fromT, err := parseDateFlag(*from)
	if err != nil {
		return fmt.Errorf("--from: %w", err)
	}
	toT, err := parseDateFlag(*to)
	if err != nil {
		return fmt.Errorf("--to: %w", err)
	}
	out, err := gworkspace.CalendarList(ctx, gs, *calID, *query, fromT, toT, *days, *max)
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func calAdd(ctx context.Context, gs *gworkspace.GoogleService, args []string) error {
	p, err := parseEventFlags(args)
	if err != nil {
		return err
	}
	out, err := gworkspace.CalendarCreate(ctx, gs, p)
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func calUpdate(ctx context.Context, gs *gworkspace.GoogleService, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: gog calendar update <event_id> [flags]")
	}
	eventID := args[0]
	p, err := parseEventFlags(args[1:])
	if err != nil {
		return err
	}
	out, err := gworkspace.CalendarUpdate(ctx, gs, eventID, p)
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func calDelete(ctx context.Context, gs *gworkspace.GoogleService, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: gog calendar delete <event_id> [--calendar ID]")
	}
	eventID := args[0]
	fs := newFlagSet()
	calID := fs.String("calendar", "")
	if _, err := fs.Parse(args[1:]); err != nil {
		return err
	}
	out, err := gworkspace.CalendarDelete(ctx, gs, *calID, eventID)
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func calConflicts(ctx context.Context, gs *gworkspace.GoogleService, args []string) error {
	fs := newFlagSet()
	from := fs.String("from", "")
	to := fs.String("to", "")
	calID := fs.String("calendar", "")
	if _, err := fs.Parse(args); err != nil {
		return err
	}
	fromT, err := parseDateFlag(*from)
	if err != nil {
		return fmt.Errorf("--from: %w", err)
	}
	toT, err := parseDateFlag(*to)
	if err != nil {
		return fmt.Errorf("--to: %w", err)
	}
	out, err := gworkspace.CalendarConflicts(ctx, gs, *calID, fromT, toT)
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func calCalendars(ctx context.Context, gs *gworkspace.GoogleService) error {
	out, err := gworkspace.CalendarCalendars(ctx, gs)
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

// ─── Helpers ────────────────────────────────────────────────────────────────────

func parseEventFlags(args []string) (gworkspace.CalendarCreateParams, error) {
	fs := newFlagSet()
	title := fs.String("title", "")
	start := fs.String("start", "")
	end := fs.String("end", "")
	dur := fs.String("duration", "")
	desc := fs.String("description", "")
	loc := fs.String("location", "")
	att := fs.String("attendees", "")
	calID := fs.String("calendar", "")
	if _, err := fs.Parse(args); err != nil {
		return gworkspace.CalendarCreateParams{}, err
	}
	p := gworkspace.CalendarCreateParams{
		CalendarID:  *calID,
		Summary:     *title,
		Start:       *start,
		End:         *end,
		Description: *desc,
		Location:    *loc,
	}
	if *dur != "" {
		d, err := time.ParseDuration(*dur)
		if err != nil {
			return p, fmt.Errorf("--duration: %w", err)
		}
		p.Duration = d
	}
	if *att != "" {
		for _, e := range strings.Split(*att, ",") {
			if e = strings.TrimSpace(e); e != "" {
				p.Attendees = append(p.Attendees, e)
			}
		}
	}
	return p, nil
}

// parseDateFlag accepts YYYY-MM-DD or full RFC3339; returns zero time when blank.
func parseDateFlag(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("expected YYYY-MM-DD or RFC3339, got %q", s)
}
