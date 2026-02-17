package ratelimiter

import (
	"database/sql"
	"time"
)

// HistoryDB defines the interface for querying rate limit history.
type HistoryDB interface {
	Query(query string, args ...interface{}) (*sql.Rows, error)
	QueryRow(query string, args ...interface{}) *sql.Row
}

// HourlyUsage represents usage aggregated by hour.
type HourlyUsage struct {
	Hour         int
	Requests     int
	InputTokens  int
	OutputTokens int
}

// DailyUsage represents usage aggregated by day.
type DailyUsage struct {
	Date         string
	Requests     int
	InputTokens  int
	OutputTokens int
}

// History provides trend analysis from historical rate limit data.
type History struct {
	db HistoryDB
}

// NewHistory creates a history analyzer.
func NewHistory(db HistoryDB) *History {
	return &History{db: db}
}

// GetHourlyPattern returns average usage by hour of day for the last N days.
func (h *History) GetHourlyPattern(days int) ([]HourlyUsage, error) {
	cutoff := time.Now().AddDate(0, 0, -days).Format("2006-01-02 15:04:05")

	rows, err := h.db.Query(
		`SELECT CAST(strftime('%H', snapshot_at) AS INTEGER) as hour,
		        AVG(requests), AVG(input_tokens), AVG(output_tokens)
		 FROM rate_limit_history
		 WHERE snapshot_at > ?
		 GROUP BY hour ORDER BY hour`,
		cutoff,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []HourlyUsage
	for rows.Next() {
		var u HourlyUsage
		if err := rows.Scan(&u.Hour, &u.Requests, &u.InputTokens, &u.OutputTokens); err != nil {
			return nil, err
		}
		result = append(result, u)
	}
	return result, rows.Err()
}

// GetDailyUsage returns daily usage totals for the last N days.
func (h *History) GetDailyUsage(days int) ([]DailyUsage, error) {
	cutoff := time.Now().AddDate(0, 0, -days).Format("2006-01-02")

	rows, err := h.db.Query(
		`SELECT date(snapshot_at) as day,
		        SUM(requests), SUM(input_tokens), SUM(output_tokens)
		 FROM rate_limit_history
		 WHERE date(snapshot_at) >= ?
		 GROUP BY day ORDER BY day`,
		cutoff,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []DailyUsage
	for rows.Next() {
		var u DailyUsage
		if err := rows.Scan(&u.Date, &u.Requests, &u.InputTokens, &u.OutputTokens); err != nil {
			return nil, err
		}
		result = append(result, u)
	}
	return result, rows.Err()
}

// GetRecentEventCount returns the count of rate limit events of a given type
// in the last N minutes.
func (h *History) GetRecentEventCount(eventType string, minutes int) (int, error) {
	cutoff := time.Now().Add(-time.Duration(minutes) * time.Minute).Format("2006-01-02 15:04:05")
	var count int
	err := h.db.QueryRow(
		"SELECT COUNT(*) FROM rate_limit_events WHERE event_type = ? AND created_at > ?",
		eventType, cutoff,
	).Scan(&count)
	return count, err
}

// GetAvgResponseTokens returns the average output tokens per request for recent history.
func (h *History) GetAvgResponseTokens(minutes int) (int, error) {
	cutoff := time.Now().Add(-time.Duration(minutes) * time.Minute).Format("2006-01-02 15:04:05")
	var avg int
	err := h.db.QueryRow(
		`SELECT COALESCE(AVG(output_tokens), 0) FROM session_history WHERE created_at > ?`,
		cutoff,
	).Scan(&avg)
	return avg, err
}
