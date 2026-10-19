package jmap

import (
	"context"
	"errors"
	"fmt"
)

// ChangesResponse is the standard /changes result (RFC 8620 section 5.2),
// shared by every object type.
type ChangesResponse struct {
	AccountID      string   `json:"accountId"`
	OldState       string   `json:"oldState"`
	NewState       string   `json:"newState"`
	HasMoreChanges bool     `json:"hasMoreChanges"`
	Created        []string `json:"created"`
	Updated        []string `json:"updated"`
	Destroyed      []string `json:"destroyed"`
}

// MailboxChangesResponse adds UpdatedProperties: when set, the updated
// mailboxes changed only in these properties (usually just the counters).
// When nil, anything may have changed.
type MailboxChangesResponse struct {
	ChangesResponse
	UpdatedProperties []string `json:"updatedProperties"`
}

// IsCannotCalculateChanges reports whether the server can no longer compute
// changes from the given state, so the caller must resync from scratch.
func IsCannotCalculateChanges(err error) bool {
	var methodErr *MethodError
	return errors.As(err, &methodErr) && methodErr.Type == "cannotCalculateChanges"
}

type changesArgs struct {
	AccountID  string `json:"accountId"`
	SinceState string `json:"sinceState"`
	MaxChanges int    `json:"maxChanges,omitempty"`
}

// GetMailboxChanges runs Mailbox/changes. maxChanges caps the ids returned
// (0 leaves it to the server); keep calling with NewState while
// HasMoreChanges is true.
func (c *Client) GetMailboxChanges(ctx context.Context, session *Session, accountID, sinceState string, maxChanges int) (*MailboxChangesResponse, error) {
	responses, err := c.Call(ctx, session, []string{CapabilityMail},
		Invocation{Name: "Mailbox/changes", Args: changesArgs{accountID, sinceState, maxChanges}, CallID: "0"})
	if err != nil {
		return nil, err
	}
	var result MailboxChangesResponse
	if err := responses.Get("0", &result); err != nil {
		return nil, fmt.Errorf("Mailbox/changes: %w", err)
	}
	return &result, nil
}
