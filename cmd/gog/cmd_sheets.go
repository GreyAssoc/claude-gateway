// sheets subcommand surface.
package main

import (
	"context"
	"fmt"

	"github.com/you/claude-gateway/internal/gworkspace"
)

const sheetsUsage = `Usage:
  gog sheets read <spreadsheet_id> --range A1:Z100
  gog sheets write <spreadsheet_id> --range A1 --values "v1,v2,v3"
  gog sheets update <spreadsheet_id> --range A1 --value v
  gog sheets append <spreadsheet_id> --range A1 --values "v1,v2"
  gog sheets clear <spreadsheet_id> --range A1:B10
  gog sheets create --title <s>
`

func runSheets(ctx context.Context, gs *gworkspace.GoogleService, args []string) error {
	if len(args) < 1 {
		fmt.Print(sheetsUsage)
		return nil
	}
	switch args[0] {
	case "read":
		return sheetsReadCmd(ctx, gs, args[1:])
	case "write":
		return sheetsWriteCmd(ctx, gs, args[1:])
	case "update":
		return sheetsUpdateCmd(ctx, gs, args[1:])
	case "append":
		return sheetsAppendCmd(ctx, gs, args[1:])
	case "clear":
		return sheetsClearCmd(ctx, gs, args[1:])
	case "create":
		return sheetsCreateCmd(ctx, gs, args[1:])
	default:
		fmt.Print(sheetsUsage)
		return fmt.Errorf("unknown sheets subcommand %q", args[0])
	}
}

func sheetsReadCmd(ctx context.Context, gs *gworkspace.GoogleService, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: gog sheets read <spreadsheet_id> --range A1:Z100")
	}
	id := args[0]
	fs := newFlagSet()
	rng := fs.String("range", "")
	if _, err := fs.Parse(args[1:]); err != nil {
		return err
	}
	out, err := gworkspace.SheetsRead(ctx, gs, id, *rng)
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func sheetsWriteCmd(ctx context.Context, gs *gworkspace.GoogleService, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: gog sheets write <spreadsheet_id> --range A1 --values 'v1,v2'")
	}
	id := args[0]
	fs := newFlagSet()
	rng := fs.String("range", "")
	values := fs.String("values", "")
	if _, err := fs.Parse(args[1:]); err != nil {
		return err
	}
	out, err := gworkspace.SheetsWrite(ctx, gs, id, *rng, *values)
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func sheetsUpdateCmd(ctx context.Context, gs *gworkspace.GoogleService, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: gog sheets update <spreadsheet_id> --range A1 --value v")
	}
	id := args[0]
	fs := newFlagSet()
	rng := fs.String("range", "")
	value := fs.String("value", "")
	if _, err := fs.Parse(args[1:]); err != nil {
		return err
	}
	out, err := gworkspace.SheetsUpdate(ctx, gs, id, *rng, *value)
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func sheetsAppendCmd(ctx context.Context, gs *gworkspace.GoogleService, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: gog sheets append <spreadsheet_id> --range A1 --values 'v1,v2'")
	}
	id := args[0]
	fs := newFlagSet()
	rng := fs.String("range", "")
	values := fs.String("values", "")
	if _, err := fs.Parse(args[1:]); err != nil {
		return err
	}
	out, err := gworkspace.SheetsAppend(ctx, gs, id, *rng, *values)
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func sheetsClearCmd(ctx context.Context, gs *gworkspace.GoogleService, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: gog sheets clear <spreadsheet_id> --range A1:B10")
	}
	id := args[0]
	fs := newFlagSet()
	rng := fs.String("range", "")
	if _, err := fs.Parse(args[1:]); err != nil {
		return err
	}
	out, err := gworkspace.SheetsClear(ctx, gs, id, *rng)
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func sheetsCreateCmd(ctx context.Context, gs *gworkspace.GoogleService, args []string) error {
	fs := newFlagSet()
	title := fs.String("title", "")
	if _, err := fs.Parse(args); err != nil {
		return err
	}
	out, err := gworkspace.SheetsCreate(ctx, gs, *title)
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}
