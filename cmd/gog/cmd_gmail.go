// gmail subcommand surface — see top of main.go for the full command list.
package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/you/claude-gateway/internal/gworkspace"
)

const gmailUsage = `Usage:
  gog gmail search '<query>' [--max N] [--label LABEL]
  gog gmail thread <thread_or_message_id>
  gog gmail send --to <addr> --subject <s> --body <b> [--attach <path>]...
  gog gmail draft create --to <addr> --subject <s> --body <b> [--cc <addr>]
  gog gmail draft reply --thread <id> --body <b> [--all]
  gog gmail labels list
  gog gmail batch modify --ids <id,id,...> [--remove-label LBL] [--add-label LBL]
`

func runGmail(ctx context.Context, gs *gworkspace.GoogleService, args []string) error {
	if len(args) < 1 {
		fmt.Print(gmailUsage)
		return nil
	}
	switch args[0] {
	case "search":
		return gmailSearch(ctx, gs, args[1:])
	case "thread", "read":
		return gmailThread(ctx, gs, args[1:])
	case "send":
		return gmailSend(ctx, gs, args[1:])
	case "draft":
		return gmailDraft(ctx, gs, args[1:])
	case "labels":
		return gmailLabels(ctx, gs, args[1:])
	case "batch":
		return gmailBatch(ctx, gs, args[1:])
	default:
		fmt.Print(gmailUsage)
		return fmt.Errorf("unknown gmail subcommand %q", args[0])
	}
}

func gmailSearch(ctx context.Context, gs *gworkspace.GoogleService, args []string) error {
	fs := newFlagSet()
	max := fs.Int("max", 10)
	label := fs.String("label", "")
	pos, err := fs.Parse(args)
	if err != nil {
		return err
	}
	if len(pos) < 1 {
		return fmt.Errorf("query required: gog gmail search '<query>'")
	}
	out, err := gworkspace.GmailSearch(ctx, gs, pos[0], *max, *label)
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func gmailThread(ctx context.Context, gs *gworkspace.GoogleService, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("thread/message id required: gog gmail thread <id>")
	}
	out, err := gworkspace.GmailThread(ctx, gs, args[0])
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func gmailSend(ctx context.Context, gs *gworkspace.GoogleService, args []string) error {
	fs := newFlagSet()
	to := fs.String("to", "")
	subject := fs.String("subject", "")
	body := fs.String("body", "")
	attaches := fs.StringSlice("attach")
	if _, err := fs.Parse(args); err != nil {
		return err
	}
	out, err := gworkspace.GmailSend(ctx, gs, *to, *subject, *body, *attaches)
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func gmailDraft(ctx context.Context, gs *gworkspace.GoogleService, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: gog gmail draft create|reply ...")
	}
	switch args[0] {
	case "create":
		fs := newFlagSet()
		to := fs.String("to", "")
		subject := fs.String("subject", "")
		body := fs.String("body", "")
		cc := fs.String("cc", "")
		if _, err := fs.Parse(args[1:]); err != nil {
			return err
		}
		out, err := gworkspace.GmailDraftCreate(ctx, gs, *to, *subject, *body, *cc)
		if err != nil {
			return err
		}
		fmt.Println(out)
		return nil
	case "reply":
		fs := newFlagSet()
		thread := fs.String("thread", "")
		body := fs.String("body", "")
		all := fs.Bool("all")
		if _, err := fs.Parse(args[1:]); err != nil {
			return err
		}
		out, err := gworkspace.GmailDraftReply(ctx, gs, *thread, *body, *all)
		if err != nil {
			return err
		}
		fmt.Println(out)
		return nil
	default:
		return fmt.Errorf("unknown draft subcommand %q (want create|reply)", args[0])
	}
}

func gmailLabels(ctx context.Context, gs *gworkspace.GoogleService, args []string) error {
	if len(args) > 0 && args[0] != "list" {
		return fmt.Errorf("usage: gog gmail labels list")
	}
	out, err := gworkspace.GmailLabels(ctx, gs)
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func gmailBatch(ctx context.Context, gs *gworkspace.GoogleService, args []string) error {
	if len(args) < 1 || args[0] != "modify" {
		return fmt.Errorf("usage: gog gmail batch modify --ids <id,id,...> [--add-label X] [--remove-label X]")
	}
	fs := newFlagSet()
	ids := fs.String("ids", "")
	add := fs.StringSlice("add-label")
	rm := fs.StringSlice("remove-label")
	if _, err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *ids == "" {
		return fmt.Errorf("--ids is required")
	}
	idList := splitCSV(*ids)
	out, err := gworkspace.GmailBatchModify(ctx, gs, idList, *add, *rm)
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
