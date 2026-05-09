// Ported from /workspace/projects/andy/tools/gsheets.go on 2026-05-09. Adapted to a CLI surface.
//
// Differences from the original:
//   - Tool wrappers stripped; each operation is a plain function.
//   - SheetsUpdate (single-cell update) and SheetsClear added — brief calls for them.
//   - parseSheetValues now also handles comma-separated values for CLI ergonomics
//     (the andy version was pipe/tab oriented since it received JSON-blob inputs).
package gworkspace

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/api/sheets/v4"
)

// SheetsRead reads a range and returns a formatted table.
func SheetsRead(ctx context.Context, gs *GoogleService, spreadsheetID, rangeStr string) (string, error) {
	svc, err := gs.SheetsService(ctx)
	if err != nil {
		return "", err
	}
	if spreadsheetID == "" || rangeStr == "" {
		return "", fmt.Errorf("spreadsheet_id and range are required")
	}
	resp, err := svc.Spreadsheets.Values.Get(spreadsheetID, rangeStr).
		ValueRenderOption("FORMATTED_VALUE").
		Do()
	if err != nil {
		return "", fmt.Errorf("gsheets read: %w", err)
	}
	if len(resp.Values) == 0 {
		return fmt.Sprintf("No data found in range '%s'.", rangeStr), nil
	}
	return formatSheetsTable(resp.Values, rangeStr), nil
}

// SheetsWrite overwrites the cells in `rangeStr` with the parsed `values`.
func SheetsWrite(ctx context.Context, gs *GoogleService, spreadsheetID, rangeStr, valuesStr string) (string, error) {
	svc, err := gs.SheetsService(ctx)
	if err != nil {
		return "", err
	}
	if spreadsheetID == "" || rangeStr == "" || valuesStr == "" {
		return "", fmt.Errorf("spreadsheet_id, range, and values are required")
	}
	rows := parseSheetValues(valuesStr)
	vr := &sheets.ValueRange{Values: rows}
	resp, err := svc.Spreadsheets.Values.Update(spreadsheetID, rangeStr, vr).
		ValueInputOption("USER_ENTERED").
		Do()
	if err != nil {
		return "", fmt.Errorf("gsheets write: %w", err)
	}
	return fmt.Sprintf("Updated **%d cells** in range `%s`.\nRows: %d, Columns: %d",
		resp.UpdatedCells, resp.UpdatedRange, resp.UpdatedRows, resp.UpdatedColumns), nil
}

// SheetsUpdate writes a single cell value. Convenience wrapper around SheetsWrite.
func SheetsUpdate(ctx context.Context, gs *GoogleService, spreadsheetID, rangeStr, value string) (string, error) {
	svc, err := gs.SheetsService(ctx)
	if err != nil {
		return "", err
	}
	if spreadsheetID == "" || rangeStr == "" {
		return "", fmt.Errorf("spreadsheet_id and range are required")
	}
	vr := &sheets.ValueRange{
		Values: [][]interface{}{{value}},
	}
	resp, err := svc.Spreadsheets.Values.Update(spreadsheetID, rangeStr, vr).
		ValueInputOption("USER_ENTERED").
		Do()
	if err != nil {
		return "", fmt.Errorf("gsheets update: %w", err)
	}
	return fmt.Sprintf("Updated cell `%s` (%d cell(s) total).", resp.UpdatedRange, resp.UpdatedCells), nil
}

// SheetsAppend appends rows after the last row of data in the range.
func SheetsAppend(ctx context.Context, gs *GoogleService, spreadsheetID, rangeStr, valuesStr string) (string, error) {
	svc, err := gs.SheetsService(ctx)
	if err != nil {
		return "", err
	}
	if spreadsheetID == "" || rangeStr == "" || valuesStr == "" {
		return "", fmt.Errorf("spreadsheet_id, range, and values are required")
	}
	rows := parseSheetValues(valuesStr)
	vr := &sheets.ValueRange{Values: rows}
	resp, err := svc.Spreadsheets.Values.Append(spreadsheetID, rangeStr, vr).
		ValueInputOption("USER_ENTERED").
		InsertDataOption("INSERT_ROWS").
		Do()
	if err != nil {
		return "", fmt.Errorf("gsheets append: %w", err)
	}
	if resp.Updates != nil {
		return fmt.Sprintf("Appended **%d rows** (%d cells) to `%s`.",
			resp.Updates.UpdatedRows, resp.Updates.UpdatedCells, resp.Updates.UpdatedRange), nil
	}
	return "Rows appended successfully.", nil
}

// SheetsClear empties the cells in `rangeStr`. NEW — not in andy.
func SheetsClear(ctx context.Context, gs *GoogleService, spreadsheetID, rangeStr string) (string, error) {
	svc, err := gs.SheetsService(ctx)
	if err != nil {
		return "", err
	}
	if spreadsheetID == "" || rangeStr == "" {
		return "", fmt.Errorf("spreadsheet_id and range are required")
	}
	resp, err := svc.Spreadsheets.Values.Clear(spreadsheetID, rangeStr, &sheets.ClearValuesRequest{}).Do()
	if err != nil {
		return "", fmt.Errorf("gsheets clear: %w", err)
	}
	return fmt.Sprintf("Cleared range `%s`.", resp.ClearedRange), nil
}

// SheetsCreate creates a new spreadsheet with the given title.
func SheetsCreate(ctx context.Context, gs *GoogleService, title string) (string, error) {
	svc, err := gs.SheetsService(ctx)
	if err != nil {
		return "", err
	}
	if title == "" {
		return "", fmt.Errorf("title is required")
	}
	ss := &sheets.Spreadsheet{
		Properties: &sheets.SpreadsheetProperties{Title: title},
	}
	created, err := svc.Spreadsheets.Create(ss).Do()
	if err != nil {
		return "", fmt.Errorf("gsheets create: %w", err)
	}
	var sheetNames []string
	for _, s := range created.Sheets {
		sheetNames = append(sheetNames, s.Properties.Title)
	}
	return fmt.Sprintf("Spreadsheet created: **%s**\nID: `%s`\nURL: %s\nSheets: %s",
		created.Properties.Title, created.SpreadsheetId, created.SpreadsheetUrl,
		strings.Join(sheetNames, ", ")), nil
}

// ─── Helpers ────────────────────────────────────────────────────────────────────

// parseSheetValues parses rows separated by newlines, columns by pipe / tab / comma.
//
// CHANGED from andy: comma is now also a valid separator. CLI users typically pass
// `--values "a,b,c"` rather than `--values "a|b|c"`. Pipes and tabs still work for
// values that contain commas.
func parseSheetValues(input string) [][]interface{} {
	input = strings.ReplaceAll(input, "\\n", "\n")
	lines := strings.Split(input, "\n")
	var rows [][]interface{}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Separator preference: pipe → tab → comma.
		var sep string
		switch {
		case strings.Contains(line, "|"):
			sep = "|"
		case strings.Contains(line, "\t"):
			sep = "\t"
		default:
			sep = ","
		}
		parts := strings.Split(line, sep)
		row := make([]interface{}, len(parts))
		for i, p := range parts {
			row[i] = strings.TrimSpace(p)
		}
		rows = append(rows, row)
	}
	return rows
}

func formatSheetsTable(values [][]interface{}, rangeStr string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("**%s** (%d rows):\n\n", rangeStr, len(values)))

	maxCols := 0
	for _, row := range values {
		if len(row) > maxCols {
			maxCols = len(row)
		}
	}
	widths := make([]int, maxCols)
	for _, row := range values {
		for i, cell := range row {
			s := fmt.Sprintf("%v", cell)
			if len(s) > widths[i] {
				widths[i] = len(s)
			}
		}
	}
	for i := range widths {
		if widths[i] > 30 {
			widths[i] = 30
		}
		if widths[i] < 3 {
			widths[i] = 3
		}
	}

	for rowIdx, row := range values {
		var cells []string
		for i := 0; i < maxCols; i++ {
			val := ""
			if i < len(row) {
				val = fmt.Sprintf("%v", row[i])
			}
			if len(val) > 30 {
				val = val[:27] + "..."
			}
			cells = append(cells, fmt.Sprintf("%-*s", widths[i], val))
		}
		b.WriteString("| " + strings.Join(cells, " | ") + " |\n")
		if rowIdx == 0 {
			var seps []string
			for _, w := range widths {
				seps = append(seps, strings.Repeat("-", w))
			}
			b.WriteString("| " + strings.Join(seps, " | ") + " |\n")
		}
	}
	return b.String()
}
