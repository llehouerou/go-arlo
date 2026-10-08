package arlo

import (
	"bufio"
	"bytes"
	"context"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message"
	_ "github.com/emersion/go-message/charset" // non-UTF-8 mail bodies
)

// IMAPCode returns a CodeFunc that waits for Arlo's code email in the INBOX
// of an IMAPS mailbox (addr is host:port). Bound the wait with ctx.
func IMAPCode(addr, user, password string) CodeFunc {
	return func(ctx context.Context, since time.Time) (string, error) {
		cl, err := imapclient.DialTLS(addr, nil)
		if err != nil {
			return "", err
		}
		defer cl.Close()
		stop := context.AfterFunc(ctx, func() { cl.Close() })
		defer stop()

		if err := cl.Login(user, password).Wait(); err != nil {
			return "", err
		}
		if _, err := cl.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
			return "", err
		}
		tick := time.NewTicker(3 * time.Second)
		defer tick.Stop()
		for {
			code, err := findCode(cl, since)
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			if err != nil || code != "" {
				return code, err
			}
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-tick.C:
			}
		}
	}
}

// findCode returns the code of the newest Arlo mail received after since,
// or "" when there is none yet.
func findCode(cl *imapclient.Client, since time.Time) (string, error) {
	if err := cl.Noop().Wait(); err != nil {
		return "", err
	}
	found, err := cl.UIDSearch(&imap.SearchCriteria{
		// SINCE only compares dates, in the server's time zone: widen it and
		// filter on the internal date below.
		Since:  since.Add(-24 * time.Hour),
		Header: []imap.SearchCriteriaHeaderField{{Key: "From", Value: "do_not_reply@arlo.com"}},
	}, nil).Wait()
	if err != nil {
		return "", err
	}
	uids := found.AllUIDs()
	if len(uids) == 0 {
		return "", nil
	}
	body := &imap.FetchItemBodySection{Peek: true}
	msgs, err := cl.Fetch(imap.UIDSetNum(uids...), &imap.FetchOptions{
		InternalDate: true,
		BodySection:  []*imap.FetchItemBodySection{body},
	}).Collect()
	if err != nil {
		return "", err
	}
	slices.SortFunc(msgs, func(a, b *imapclient.FetchMessageBuffer) int {
		return b.InternalDate.Compare(a.InternalDate)
	})
	for _, m := range msgs {
		// A minute of slack for clock skew between us and the mail server.
		if m.InternalDate.Before(since.Add(-time.Minute)) {
			break
		}
		if code := codeFromMail(m.FindBodySection(body)); code != "" {
			return code, nil
		}
	}
	return "", nil
}

// Arlo's mail puts the code alone on a line.
var codeLine = regexp.MustCompile(`^\W*(\d{6})\W*$`)

// codeFromMail finds the code in the text parts of a raw email.
func codeFromMail(raw []byte) string {
	e, err := message.Read(bytes.NewReader(raw))
	if err != nil && !message.IsUnknownCharset(err) {
		return ""
	}
	code := ""
	_ = e.Walk(func(_ []int, part *message.Entity, err error) error {
		if err != nil || code != "" {
			return nil
		}
		t, _, _ := part.Header.ContentType()
		if t != "text/plain" && t != "text/html" {
			return nil
		}
		sc := bufio.NewScanner(part.Body)
		for sc.Scan() {
			if m := codeLine.FindStringSubmatch(strings.TrimSpace(sc.Text())); m != nil {
				code = m[1]
				return nil
			}
		}
		return nil
	})
	return code
}
