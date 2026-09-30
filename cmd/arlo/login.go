package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"time"

	arlo "github.com/llehouerou/go-arlo"
)

func login(args []string) error {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	email := fs.String("email", "", "Arlo account email")
	passwordFile := fs.String("password-file", "", "file holding the Arlo password")
	sessionPath := fs.String("session", "arlo.session.json", "session file")
	imapAddr := fs.String("imap-addr", "imap.example.com:993", "IMAPS server receiving Arlo's 2FA emails")
	imapUser := fs.String("imap-user", "", "IMAP login; empty to type the 2FA code instead")
	imapPasswordFile := fs.String("imap-password-file", "", "file holding the IMAP password")
	dump := fs.String("dump", "debug", "directory for redacted response dumps; empty to disable")
	_ = fs.Parse(args)
	if *email == "" || *passwordFile == "" {
		return errors.New("login: -email and -password-file are required")
	}
	password, err := readSecret(*passwordFile)
	if err != nil {
		return err
	}

	code := typedCode
	if *imapUser != "" {
		imapPassword, err := readSecret(*imapPasswordFile)
		if err != nil {
			return err
		}
		fromMail := arlo.IMAPCode(*imapAddr, *imapUser, imapPassword)
		code = func(ctx context.Context, since time.Time) (string, error) {
			ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
			defer cancel()
			slog.Info("waiting for Arlo's code email", "mailbox", *imapUser)
			return fromMail(ctx, since)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	c := arlo.New(arlo.Config{
		Email:       *email,
		Password:    password,
		SessionPath: *sessionPath,
		Code:        code,
		DumpDir:     *dump,
	})
	if err := c.Login(ctx); err != nil {
		return err
	}
	fmt.Println("logged in; session in", *sessionPath)
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
