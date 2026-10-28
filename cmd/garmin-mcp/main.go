package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/danchupin/garmin-personal-mcp/internal/app"
	"github.com/danchupin/garmin-personal-mcp/internal/broker"
	"github.com/danchupin/garmin-personal-mcp/internal/domain"
	"github.com/danchupin/garmin-personal-mcp/internal/mcpserver"
	"github.com/danchupin/garmin-personal-mcp/internal/personal"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/term"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, app.SafeMessage(err))
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string) error {
	command := "stdio"
	if len(arguments) == 1 {
		command = arguments[0]
	}
	if len(arguments) > 1 {
		return domain.NewError("CONFIGURATION", "Use stdio or login without positional options.", 500)
	}
	switch command {
	case "login":
		return localLogin(ctx)
	case "stdio":
		return stdio(ctx)
	default:
		return domain.NewError("CONFIGURATION", "Use stdio or login.", 500)
	}
}

func region() string {
	value := os.Getenv("GARMIN_REGION")
	if value == "" {
		return "global"
	}
	return value
}
func stdio(ctx context.Context) error {
	scopes := strings.Fields(os.Getenv("GARMIN_SCOPES"))
	if len(scopes) == 0 {
		scopes = []string{"wellness:read", "activities:read"}
	}
	for _, scope := range scopes {
		if !slices.Contains(personal.Scopes(), scope) {
			return domain.NewError("CONFIGURATION", "An unknown local permission was requested.", 500)
		}
	}
	p := domain.Principal{Issuer: "local", Subject: "stdio", Scopes: scopes, IssuedAt: time.Now().UTC()}
	options := broker.Options{}
	if os.Getenv("GARMIN_ACCESS_TOKEN") != "" || os.Getenv("GARMIN_ACCESS_TOKEN_FILE") != "" {
		token, err := app.LocalToken(os.Getenv)
		if err != nil {
			return err
		}
		if _, err = personal.NewAccess(token, region(), personal.Options{}); err != nil {
			return err
		}
		expires, err := personal.TokenExpiry(token)
		if err != nil {
			return err
		}
		p.Credential = &domain.Credential{Token: token, Region: region(), ExpiresAt: expires}
		p.ExpiresAt = expires.Add(-time.Minute)
	} else {
		path, err := app.LocalKeyPath(os.Getenv)
		if err != nil {
			return err
		}
		getenv := func(name string) string {
			if name == "GARMIN_PERSONAL_API_KEY_FILE" && os.Getenv("GARMIN_PERSONAL_API_KEY") == "" {
				return path
			}
			return os.Getenv(name)
		}
		options.LocalCredential, err = personal.NewLocalKeySource(func() (string, error) { return app.LocalKey(getenv) }, region(), personal.Options{})
		if err != nil {
			return err
		}
	}
	service, err := broker.New(options)
	if err != nil {
		return err
	}
	defer service.Close()
	return mcpserver.New(service, p).Run(ctx, &mcp.StdioTransport{MaxLineLength: 65536})
}

func localLogin(ctx context.Context) error {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return domain.NewError("CONFIGURATION", "Run login in an interactive terminal; the key is read without echo.", 500)
	}
	path, err := app.LocalKeyPath(os.Getenv)
	if err != nil {
		return err
	}
	fmt.Fprint(os.Stderr, "Garmin Personal API key (hidden input): ")
	key, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return domain.NewError("CREDENTIAL_INPUT", "Could not read the local credential.", 500)
	}
	defer clear(key)
	key = bytes.TrimSpace(key)
	if _, err = personal.Exchange(ctx, string(key), region(), personal.Options{}); err != nil {
		return err
	}
	if err = app.SaveLocalKey(path, key); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "Key verified with the official Garmin API and saved in a private local file. Neither the key nor the temporary token was printed.")
	return nil
}
