package mail

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/cristianadrielbraun/gofer/internal/mail/jmap"
	"github.com/cristianadrielbraun/gofer/internal/models"
)

// JMAPNotSentError is a JMAP send that failed before the server could accept
// the message, so nothing went out. Retryable says whether trying again may
// work, as when the server was unreachable, rather than being refused.
type JMAPNotSentError struct {
	Err       error
	Retryable bool
}

func (e *JMAPNotSentError) Error() string { return e.Err.Error() }
func (e *JMAPNotSentError) Unwrap() error { return e.Err }

// Submission refusals worth trying again later; any other refusal stands.
var retryableJMAPSubmissionErrors = map[string]bool{"rateLimit": true, "serverUnavailable": true, "serverFail": true}

// SendJMAPMessage sends one message Gofer built, from the account's identity
// for from, to every address in to, including Bcc recipients. It returns the
// sent email's id. A *JMAPNotSentError means nothing was sent; any other error
// may follow the server accepting the message, so it must not be retried
// blindly.
func SendJMAPMessage(ctx context.Context, cfg *models.AccountConfig, password, from string, to []string, raw []byte) (string, error) {
	notSent := func(err error, retryable bool) error { return &JMAPNotSentError{Err: err, Retryable: retryable} }
	client, err := newJMAPAccountClient(cfg, password)
	if err != nil {
		return "", notSent(err, false)
	}
	session, err := client.Session(ctx, cfg.JMAPSessionURL)
	if err != nil {
		var httpErr *jmap.HTTPError
		rejected := errors.As(err, &httpErr) && (httpErr.StatusCode == http.StatusUnauthorized || httpErr.StatusCode == http.StatusForbidden)
		return "", notSent(fmt.Errorf("JMAP session: %w", err), !rejected)
	}
	account := session.PrimaryAccount(jmap.CapabilityMail)
	if account == "" {
		return "", notSent(errors.New("the JMAP server offers no mail account for this login"), false)
	}
	if _, ok := session.Capabilities[jmap.CapabilitySubmission]; !ok {
		return "", notSent(errors.New("the JMAP server does not offer sending"), false)
	}
	identities, err := client.GetIdentities(ctx, session, account)
	if err != nil {
		return "", notSent(err, true)
	}
	identity, ok := jmap.MatchIdentity(identities.List, from)
	if !ok {
		return "", notSent(fmt.Errorf("the JMAP server does not let this account send as %s", from), false)
	}
	mailboxes, err := client.GetMailboxes(ctx, session, account, nil)
	if err != nil {
		return "", notSent(err, true)
	}
	var drafts, sent string
	for _, mailbox := range mailboxes.List {
		switch strings.ToLower(mailbox.Role) {
		case "drafts":
			drafts = mailbox.ID
		case "sent":
			sent = mailbox.ID
		}
	}
	if drafts == "" {
		return "", notSent(errors.New("the JMAP account has no Drafts mailbox to send from"), false)
	}
	blob, err := client.UploadBlob(ctx, session, account, jmap.ContentTypeMessage, raw)
	if err != nil {
		var httpErr *jmap.HTTPError
		tooLarge := errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusRequestEntityTooLarge
		tooLarge = tooLarge || strings.Contains(err.Error(), "upload limit")
		return "", notSent(fmt.Errorf("upload message: %w", err), !tooLarge)
	}
	result, err := client.SendEmail(ctx, session, account, jmap.Outgoing{
		BlobID: blob, IdentityID: identity.ID, DraftsID: drafts, SentID: sent,
		Envelope: jmap.Envelope{MailFrom: from, RcptTo: to},
	})
	var refused *jmap.SubmissionError
	var httpErr *jmap.HTTPError
	switch {
	case errors.As(err, &refused):
		return "", notSent(err, retryableJMAPSubmissionErrors[refused.Type])
	case errors.As(err, &httpErr):
		// The server answered the request with an error, so it ran no part of it.
		return "", notSent(err, httpErr.StatusCode >= 500 || httpErr.StatusCode == http.StatusTooManyRequests)
	case err != nil:
		return "", err
	}
	return result.EmailID, nil
}
