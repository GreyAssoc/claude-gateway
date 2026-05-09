// drive subcommand surface.
package main

import (
	"context"
	"fmt"

	"github.com/you/claude-gateway/internal/gworkspace"
)

const driveUsage = `Usage:
  gog drive ls [--parent ID] [--max N]
  gog drive search '<query>' [--max N]
  gog drive download <file_id> --out <path> [--export-format pdf|docx|xlsx|csv|txt]
  gog drive upload <path> --parent <folder_id> [--name <override>]
  gog drive mkdir <name> --parent <folder_id>
  gog drive info <file_id>
  gog drive delete <file_id>
  gog drive trash <file_id>
  gog drive move <file_id> --to <folder_id>
`

func runDrive(ctx context.Context, gs *gworkspace.GoogleService, args []string) error {
	if len(args) < 1 {
		fmt.Print(driveUsage)
		return nil
	}
	switch args[0] {
	case "ls", "list":
		return driveLs(ctx, gs, args[1:])
	case "search":
		return driveSearch(ctx, gs, args[1:])
	case "download":
		return driveDownload(ctx, gs, args[1:])
	case "upload":
		return driveUpload(ctx, gs, args[1:])
	case "mkdir":
		return driveMkdir(ctx, gs, args[1:])
	case "info":
		return driveInfo(ctx, gs, args[1:])
	case "trash":
		return driveTrash(ctx, gs, args[1:])
	case "delete":
		return driveDelete(ctx, gs, args[1:])
	case "move":
		return driveMove(ctx, gs, args[1:])
	default:
		fmt.Print(driveUsage)
		return fmt.Errorf("unknown drive subcommand %q", args[0])
	}
}

func driveLs(ctx context.Context, gs *gworkspace.GoogleService, args []string) error {
	fs := newFlagSet()
	parent := fs.String("parent", "")
	max := fs.Int("max", 25)
	if _, err := fs.Parse(args); err != nil {
		return err
	}
	out, err := gworkspace.DriveLs(ctx, gs, *parent, *max)
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func driveSearch(ctx context.Context, gs *gworkspace.GoogleService, args []string) error {
	fs := newFlagSet()
	max := fs.Int("max", 20)
	pos, err := fs.Parse(args)
	if err != nil {
		return err
	}
	if len(pos) < 1 {
		return fmt.Errorf("query required: gog drive search '<query>'")
	}
	out, err := gworkspace.DriveSearch(ctx, gs, pos[0], *max)
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func driveDownload(ctx context.Context, gs *gworkspace.GoogleService, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: gog drive download <file_id> --out <path>")
	}
	fileID := args[0]
	fs := newFlagSet()
	out := fs.String("out", "")
	exportFmt := fs.String("export-format", "pdf")
	if _, err := fs.Parse(args[1:]); err != nil {
		return err
	}
	res, err := gworkspace.DriveDownload(ctx, gs, fileID, *out, *exportFmt)
	if err != nil {
		return err
	}
	fmt.Println(res)
	return nil
}

func driveUpload(ctx context.Context, gs *gworkspace.GoogleService, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: gog drive upload <path> --parent <folder_id> [--name <override>]")
	}
	path := args[0]
	fs := newFlagSet()
	parent := fs.String("parent", "")
	name := fs.String("name", "")
	if _, err := fs.Parse(args[1:]); err != nil {
		return err
	}
	out, err := gworkspace.DriveUpload(ctx, gs, path, *parent, *name)
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func driveMkdir(ctx context.Context, gs *gworkspace.GoogleService, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: gog drive mkdir <name> --parent <folder_id>")
	}
	name := args[0]
	fs := newFlagSet()
	parent := fs.String("parent", "")
	if _, err := fs.Parse(args[1:]); err != nil {
		return err
	}
	out, err := gworkspace.DriveMkdir(ctx, gs, name, *parent)
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func driveInfo(ctx context.Context, gs *gworkspace.GoogleService, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: gog drive info <file_id>")
	}
	out, err := gworkspace.DriveInfo(ctx, gs, args[0])
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func driveTrash(ctx context.Context, gs *gworkspace.GoogleService, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: gog drive trash <file_id>")
	}
	out, err := gworkspace.DriveTrash(ctx, gs, args[0])
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func driveDelete(ctx context.Context, gs *gworkspace.GoogleService, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: gog drive delete <file_id>")
	}
	out, err := gworkspace.DriveDelete(ctx, gs, args[0])
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func driveMove(ctx context.Context, gs *gworkspace.GoogleService, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: gog drive move <file_id> --to <folder_id>")
	}
	fileID := args[0]
	fs := newFlagSet()
	to := fs.String("to", "")
	if _, err := fs.Parse(args[1:]); err != nil {
		return err
	}
	out, err := gworkspace.DriveMove(ctx, gs, fileID, *to)
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}
