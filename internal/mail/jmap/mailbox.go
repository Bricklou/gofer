package jmap

import (
	"context"
	"fmt"
)

// GetResponse is the standard /get result (RFC 8620 section 5.1), shared by
// every object type.
type GetResponse[T any] struct {
	AccountID string   `json:"accountId"`
	State     string   `json:"state"`
	List      []T      `json:"list"`
	NotFound  []string `json:"notFound"`
}

// Mailbox is RFC 8621 section 2. ParentID and Role are "" when null.
type Mailbox struct {
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	ParentID      string        `json:"parentId"`
	Role          string        `json:"role"`
	SortOrder     int           `json:"sortOrder"`
	TotalEmails   int           `json:"totalEmails"`
	UnreadEmails  int           `json:"unreadEmails"`
	TotalThreads  int           `json:"totalThreads"`
	UnreadThreads int           `json:"unreadThreads"`
	MyRights      MailboxRights `json:"myRights"`
	IsSubscribed  bool          `json:"isSubscribed"`
}

type MailboxRights struct {
	MayReadItems   bool `json:"mayReadItems"`
	MayAddItems    bool `json:"mayAddItems"`
	MayRemoveItems bool `json:"mayRemoveItems"`
	MaySetSeen     bool `json:"maySetSeen"`
	MaySetKeywords bool `json:"maySetKeywords"`
	MayCreateChild bool `json:"mayCreateChild"`
	MayRename      bool `json:"mayRename"`
	MayDelete      bool `json:"mayDelete"`
	MaySubmit      bool `json:"maySubmit"`
}

// GetMailboxes runs Mailbox/get. A nil ids fetches every mailbox; an empty,
// non-nil ids fetches none and only returns the current state.
func (c *Client) GetMailboxes(ctx context.Context, session *Session, accountID string, ids []string) (*GetResponse[Mailbox], error) {
	args := struct {
		AccountID string   `json:"accountId"`
		IDs       []string `json:"ids"`
	}{accountID, ids}
	responses, err := c.Call(ctx, session, []string{CapabilityMail},
		Invocation{Name: "Mailbox/get", Args: args, CallID: "0"})
	if err != nil {
		return nil, err
	}
	var result GetResponse[Mailbox]
	if err := responses.Get("0", &result); err != nil {
		return nil, fmt.Errorf("Mailbox/get: %w", err)
	}
	return &result, nil
}
