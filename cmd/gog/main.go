// gog — in-tree replacement for steipete/gogcli. Routes CLI commands to the
// gworkspace package, which talks to Google Workspace APIs using a token
// stored at $GOG_TOKEN_PATH (default /root/.config/gog/token.json).
//
// Source surface to match (configs/claude-code-settings.json + memory/bootstrap/
// environment.md reference these commands and are not changed in this port):
//
//   gog auth url | exchange <code> | status
//   gog gmail search | thread | send | draft create | draft reply | labels list | batch modify
//   gog calendar list | add | update | delete | calendars | conflicts
//   gog drive ls | search | download | upload | mkdir | info | delete | trash | move
//   gog sheets read | write | update | append | clear | create
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/you/claude-gateway/internal/gworkspace"
)

const usage = `gog — Google Workspace CLI for claude-gateway

Usage:
  gog <resource> <subcommand> [flags] [args]

Resources:
  auth       OAuth flow + token status
  gmail      Mail search, threads, drafts, send, labels, batch modify
  calendar   List, create, update, delete events; calendars; conflict scan
  drive      List, search, download, upload, mkdir, info, trash, delete, move
  sheets     Read, write, update, append, clear, create

Environment:
  GOOGLE_CLIENT_ID       OAuth client ID (required)
  GOOGLE_CLIENT_SECRET   OAuth client secret (required)
  GOG_TOKEN_PATH         Path to token.json (default /root/.config/gog/token.json)

Examples:
  gog auth url
  gog auth exchange 4/0AeaY...
  gog auth status
  gog gmail search 'is:unread' --max 5
  gog gmail send --to alice@example.com --subject Hello --body 'Hi'
  gog calendar list --days 14
  gog drive ls --parent root
  gog sheets read 1A2b3C... --range Sheet1!A1:D10

Run "gog <resource>" with no subcommand for resource-specific help.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(0)
	}

	switch os.Args[1] {
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	case "--version", "-v", "version":
		fmt.Println("gog (in-tree) for claude-gateway")
		return
	}

	resource := os.Args[1]
	rest := os.Args[2:]

	// Resource-level help: if no subcommand is given, print resource usage and
	// exit before requiring credentials. This lets users discover the surface
	// without GOOGLE_CLIENT_ID set.
	if len(rest) == 0 || rest[0] == "-h" || rest[0] == "--help" {
		printResourceUsage(resource)
		return
	}

	gs, err := newService()
	if err != nil {
		fmt.Fprintf(os.Stderr, "gog: %v\n", err)
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	var runErr error
	switch resource {
	case "auth":
		runErr = runAuth(ctx, gs, rest)
	case "gmail":
		runErr = runGmail(ctx, gs, rest)
	case "calendar", "cal":
		runErr = runCalendar(ctx, gs, rest)
	case "drive":
		runErr = runDrive(ctx, gs, rest)
	case "sheets":
		runErr = runSheets(ctx, gs, rest)
	default:
		fmt.Fprintf(os.Stderr, "gog: unknown resource %q\n\n%s", resource, usage)
		os.Exit(2)
	}

	if runErr != nil {
		fmt.Fprintf(os.Stderr, "gog %s: %v\n", resource, runErr)
		os.Exit(1)
	}
}

func printResourceUsage(resource string) {
	switch resource {
	case "auth":
		fmt.Println(`Usage:
  gog auth url                  Print OAuth authorisation URL
  gog auth exchange <code>      Exchange auth code for token
  gog auth status               Show current auth status`)
	case "gmail":
		fmt.Print(gmailUsage)
	case "calendar", "cal":
		fmt.Print(calUsage)
	case "drive":
		fmt.Print(driveUsage)
	case "sheets":
		fmt.Print(sheetsUsage)
	default:
		fmt.Fprintf(os.Stderr, "gog: unknown resource %q\n\n%s", resource, usage)
		os.Exit(2)
	}
}

func newService() (*gworkspace.GoogleService, error) {
	clientID := os.Getenv("GOOGLE_CLIENT_ID")
	clientSecret := os.Getenv("GOOGLE_CLIENT_SECRET")
	tokenPath := os.Getenv("GOG_TOKEN_PATH")
	if tokenPath == "" {
		tokenPath = "/root/.config/gog/token.json"
	}
	return gworkspace.NewGoogleService(gworkspace.GoogleConfig{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		TokenPath:    tokenPath,
	})
}
