package jmap

import (
	"context"
	"errors"
	"fmt"
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
