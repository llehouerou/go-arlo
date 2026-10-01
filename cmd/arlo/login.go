package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	arlo "github.com/llehouerou/go-arlo"
)

// clientFlags declares the flags every command talking to Arlo needs and
// returns the function building the client once they are parsed.
func clientFlags(fs *flag.FlagSet) func() (*arlo.Client, error) {
	email := fs.String("email", "", "Arlo account email")
	passwordFile := fs.String("password-file", "", "file holding the Arlo password")
	sessionPath := fs.String("session", "arlo.session.json", "session file")
	imapAddr := fs.String("imap-addr", "", "IMAPS server (host:port) receiving Arlo's 2FA emails")
	imapUser := fs.String("imap-user", "", "IMAP login; empty to type the 2FA code instead")
	imapPasswordFile := fs.String("imap-password-file", "", "file holding the IMAP password")
	dump := fs.String("dump", "debug", "directory for redacted response dumps; empty to disable")
	verbose := fs.Bool("v", false, "debug logs")

	return func() (*arlo.Client, error) {
		if *verbose {
			slog.SetLogLoggerLevel(slog.LevelDebug)
		}
		if *email == "" || *passwordFile == "" {
			return nil, errors.New("-email and -password-file are required")
		}
		password, err := readSecret(*passwordFile)
		if err != nil {
			return nil, err
		}
		code := typedCode
		if *imapUser != "" {
			if *imapAddr == "" {
				return nil, errors.New("-imap-user needs -imap-addr")
			}
			imapPassword, err := readSecret(*imapPasswordFile)
			if err != nil {
				return nil, err
			}
			fromMail := arlo.IMAPCode(*imapAddr, *imapUser, imapPassword)
			code = func(ctx context.Context, since time.Time) (string, error) {
				ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
				defer cancel()
				slog.Info("waiting for Arlo's code email", "mailbox", *imapUser)
				return fromMail(ctx, since)
			}
		}
		return arlo.New(arlo.Config{
			Email:       *email,
			Password:    password,
			SessionPath: *sessionPath,
			Code:        code,
			DumpDir:     *dump,
		}), nil
	}
}

func login(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	client := clientFlags(fs)
	_ = fs.Parse(args)
	c, err := client()
	if err != nil {
		return err
	}
	if err := c.Login(ctx); err != nil {
		return err
	}
	fmt.Println("logged in")
	return nil
}

func typedCode(ctx context.Context, _ time.Time) (string, error) {
	fmt.Fprint(os.Stderr, "Code Arlo emailed: ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(line), ctx.Err()
}

func readSecret(path string) (string, error) {
	if path == "" {
		return "", errors.New("missing secret file path")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}
