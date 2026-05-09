// auth subcommand: gog auth {url, exchange <code>, status}.
package main

import (
	"context"
	"fmt"

	"github.com/you/claude-gateway/internal/gworkspace"
)

func runAuth(ctx context.Context, gs *gworkspace.GoogleService, args []string) error {
	if len(args) < 1 {
		fmt.Println(`Usage:
  gog auth url                  Print OAuth authorisation URL
  gog auth exchange <code>      Exchange auth code for token
  gog auth status               Show current auth status`)
		return nil
	}

	switch args[0] {
	case "url":
		fmt.Println(gs.AuthURL())
		return nil
	case "exchange":
		if len(args) < 2 {
			return fmt.Errorf("exchange requires a code argument")
		}
		if err := gs.ExchangeCode(ctx, args[1]); err != nil {
			return err
		}
		fmt.Println("Token saved.")
		return nil
	case "status":
		fmt.Println(gs.Status())
		return nil
	default:
		return fmt.Errorf("unknown auth subcommand %q", args[0])
	}
}
