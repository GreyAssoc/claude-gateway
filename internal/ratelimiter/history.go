package ratelimiter

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// historyDB defines the interface for querying rate limit history.
type historyDB interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// hourlyUsage represents usage aggregated by hour.
type hourlyUsage struct {
	Hour         int
	Requests     int
	InputTokens  int
	OutputTokens int
}

// dailyUsage represents usage aggregated by day.
type dailyUsage struct {
	Date         string
	Requests     int
	InputTokens  int
	OutputTokens int
}

// history provides trend analysis from historical rate limit data.
type history struct {
	db historyDB
}

// newHistory creates a history analyzer.
func newHistory(db historyDB) (*history, error) {
	if db == nil {
		return nil, errors.New("ratelimiter: newHistory called with nil db")
	}
	return &history{db: db}, nil
}

// getHourlyPattern returns average usage by hour of day for the last N days.
func (h *history) getHourlyPattern(ctx context.Context, days int) ([]hourlyUsage, error) {
	if days <= 0 {
		return nil, nil
	}
	if days > 365 {
		days = 365
	}
	cutoff := time.Now().AddDate(0, 0, -days).Format("2006-01-02 15:04:05")

	rows, err := h.db.QueryContext(ctx,
		`SELECT CAST(strftime('%H', snapshot_at) AS INTEGER) as hour,
		        AVG(requests), AVG(input_tokens), AVG(output_tokens)
		 FROM rate_limit_history
		 WHERE snapshot_at > ?
		 GROUP BY hour ORDER BY hour`,
		cutoff,
	)
	if err != nil {
		return nil, fmt.Errorf("querying hourly pattern: %w", err)
	}
	defer rows.Close()

	var result []hourlyUsage
	for rows.Next() {
		var u hourlyUsage
		if err := rows.Scan(&u.Hour, &u.Requests, &u.InputTokens, &u.OutputTokens); err != nil {
			return nil, fmt.Errorf("scanning hourly usage: %w", err)
		}
		result = append(result, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating hourly pattern rows: %w", err)
	}
	return result, nil
}

// getDailyUsage returns daily usage totals for the last N days.
func (h *history) getDailyUsage(ctx context.Context, days int) ([]dailyUsage, error) {
	if days <= 0 {
		return nil, nil
	}
	if days > 365 {
		days = 365
	}
	cutoff := time.Now().AddDate(0, 0, -days).Format("2006-01-02")

	rows, err := h.db.QueryContext(ctx,
		`SELECT date(snapshot_at) as day,
		        SUM(requests), SUM(input_tokens), SUM(output_tokens)
		 FROM rate_limit_history
		 WHERE date(snapshot_at) >= ?
		 GROUP BY day ORDER BY day`,
		cutoff,
	)
	if err != nil {
		return nil, fmt.Errorf("querying daily usage: %w", err)
	}
	defer rows.Close()

	var result []dailyUsage
	for rows.Next() {
		var u dailyUsage
		if err := rows.Scan(&u.Date, &u.Requests, &u.InputTokens, &u.OutputTokens); err != nil {
			return nil, fmt.Errorf("scanning daily usage: %w", err)
		}
		result = append(result, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating daily usage rows: %w", err)
	}
	return result, nil
}

// getRecentEventCount returns the count of rate limit events of a given type
// in the last N minutes.
func (h *history) getRecentEventCount(ctx context.Context, eventType string, minutes int) (int, error) {
	if minutes <= 0 {
		return 0, nil
	}
	cutoff := time.Now().Add(-time.Duration(minutes) * time.Minute).Format("2006-01-02 15:04:05")
	var count int
	err := h.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM rate_limit_events WHERE event_type = ? AND created_at > ?",
		eventType, cutoff,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("querying recent event count: %w", err)
	}
	return count, nil
}

// getAvgResponseTokens returns the average output tokens per request for recent history.
func (h *history) getAvgResponseTokens(ctx context.Context, minutes int) (int, error) {
	if minutes <= 0 {
		return 0, nil
	}
	cutoff := time.Now().Add(-time.Duration(minutes) * time.Minute).Format("2006-01-02 15:04:05")
	var avg int
	err := h.db.QueryRowContext(ctx,
		`SELECT COALESCE(AVG(output_tokens), 0) FROM session_history WHERE created_at > ?`,
		cutoff,
	).Scan(&avg)
	if err != nil {
		return 0, fmt.Errorf("querying avg response tokens: %w", err)
	}
	return avg, nil
}
