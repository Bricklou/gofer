package jmap

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"
)

// ImportError is the server refusing to store a message or create a mailbox.
// Nothing was stored, and sending the same request again won't change that.
type ImportError struct {
	Step string
	SetError
}

func (e *ImportError) Error() string {
	return "jmap: " + e.Step + ": " + e.SetError.Error()
}

// Imported is a message stored by ImportEmail. Existed says the server
// already had it and returned that copy instead of storing another.
type Imported struct {
	ID      string
	Existed bool
}

// ImportEmail stores an uploaded message in one mailbox with the keywords
// given, received at receivedAt when it isn't zero, so it sorts by its own
// date rather than the time of the import. A server that keeps one copy of
// each message answers alreadyExists for one it has; that copy is returned.
func (c *Client) ImportEmail(ctx context.Context, session *Session, accountID, blobID, mailboxID string, keywords []string, receivedAt time.Time) (*Imported, error) {
	if blobID == "" || mailboxID == "" {
		return nil, errors.New("jmap: importing a message needs a blob and a mailbox")
	}
	email := map[string]any{
		"blobId":     blobID,
		"mailboxIds": map[string]bool{mailboxID: true},
	}
	set := map[string]bool{}
	for _, keyword := range keywords {
		set[keyword] = true
	}
	email["keywords"] = set
	if !receivedAt.IsZero() {
		email["receivedAt"] = receivedAt.UTC().Format(time.RFC3339)
	}
	responses, err := c.Call(ctx, session, []string{CapabilityMail},
		Invocation{Name: "Email/import", Args: map[string]any{
			"accountId": accountID,
			"emails":    map[string]any{"email": email},
		}, CallID: "0"})
	if err != nil {
		return nil, err
	}
	var imported struct {
		Created    map[string]struct{ ID string } `json:"created"`
		NotCreated map[string]SetError            `json:"notCreated"`
	}
	if err := responses.Get("0", &imported); err != nil {
		return nil, fmt.Errorf("Email/import: %w", err)
	}
	if failure, ok := imported.NotCreated["email"]; ok {
		if failure.Type == "alreadyExists" && failure.ExistingID != "" {
			return &Imported{ID: failure.ExistingID, Existed: true}, nil
		}
		return nil, &ImportError{Step: "import", SetError: failure}
	}
	id := imported.Created["email"].ID
	if id == "" {
		return nil, errors.New("jmap: the server did not confirm the import")
	}
	return &Imported{ID: id}, nil
}

// Window is a span of receivedAt times, From included and To not.
type Window struct {
	From, To time.Time
}

// findEmailsLimit is how many emails each FindEmails search reads.
const findEmailsLimit = 100

// FindEmails returns the emails in a mailbox whose Message-ID is messageID,
// given without angle brackets, to tell whether a message whose import got
// no answer was stored. It searches by the Message-ID header and by each
// receivedAt window in one request, as far as the server's call limit
// allows: header search is optional and may lag behind a message just
// stored, and a server may not keep the receivedAt it was given. Results are
// compared by Message-ID exactly. It fails only when every search fails.
func (c *Client) FindEmails(ctx context.Context, session *Session, accountID, mailboxID, messageID string, windows []Window) ([]string, error) {
	if mailboxID == "" || messageID == "" {
		return nil, errors.New("jmap: finding an email needs a mailbox and a Message-ID")
	}
	filters := []*EmailFilter{{InMailbox: mailboxID, Header: []string{"Message-ID", messageID}}}
	for _, window := range windows {
		from, to := window.From.UTC().Truncate(time.Second), window.To.UTC().Truncate(time.Second).Add(time.Second)
		filters = append(filters, &EmailFilter{InMailbox: mailboxID, After: &from, Before: &to})
	}
	perRequest := len(filters)
	if limit := session.Core.MaxCallsInRequest; limit > 0 {
		perRequest = max(1, min(perRequest, limit/2))
	}
	var found []string
	var failures []error
	answered := 0
	for start := 0; start < len(filters); start += perRequest {
		batch := filters[start:min(start+perRequest, len(filters))]
		var calls []Invocation
		for i, filter := range batch {
			query, get := fmt.Sprintf("q%d", i), fmt.Sprintf("g%d", i)
			calls = append(calls,
				Invocation{Name: "Email/query", Args: EmailQuery{AccountID: accountID, Filter: filter,
					Sort: []Comparator{{Property: "receivedAt"}}, Limit: findEmailsLimit}, CallID: query},
				Invocation{Name: "Email/get", Args: map[string]any{
					"accountId":  accountID,
					"#ids":       ResultReference{ResultOf: query, Name: "Email/query", Path: "/ids"},
					"properties": []string{"id", "messageId"},
				}, CallID: get},
			)
		}
		responses, err := c.Call(ctx, session, []string{CapabilityMail}, calls...)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		for i := range batch {
			var emails GetResponse[Email]
			if err := responses.Get(fmt.Sprintf("g%d", i), &emails); err != nil {
				failures = append(failures, fmt.Errorf("Email/get: %w", err))
				continue
			}
			answered++
			for _, email := range emails.List {
				for _, id := range email.MessageID {
					if id == messageID && !slices.Contains(found, email.ID) {
						found = append(found, email.ID)
					}
				}
			}
		}
	}
	if answered == 0 {
		return nil, errors.Join(failures...)
	}
	return found, nil
}

// CreateMailbox creates a mailbox named name inside parentID, or at the top
// when parentID is "", and returns its id.
func (c *Client) CreateMailbox(ctx context.Context, session *Session, accountID, name, parentID string) (string, error) {
	if name == "" {
		return "", errors.New("jmap: a mailbox needs a name")
	}
	mailbox := map[string]any{"name": name, "parentId": nil}
	if parentID != "" {
		mailbox["parentId"] = parentID
	}
	responses, err := c.Call(ctx, session, []string{CapabilityMail},
		Invocation{Name: "Mailbox/set", Args: map[string]any{
			"accountId": accountID,
			"create":    map[string]any{"mailbox": mailbox},
		}, CallID: "0"})
	if err != nil {
		return "", err
	}
	var created struct {
		Created    map[string]struct{ ID string } `json:"created"`
		NotCreated map[string]SetError            `json:"notCreated"`
	}
	if err := responses.Get("0", &created); err != nil {
		return "", fmt.Errorf("Mailbox/set: %w", err)
	}
	if failure, ok := created.NotCreated["mailbox"]; ok {
		return "", &ImportError{Step: "create mailbox", SetError: failure}
	}
	id := created.Created["mailbox"].ID
	if id == "" {
		return "", errors.New("jmap: the server did not confirm the mailbox")
	}
	return id, nil
}
