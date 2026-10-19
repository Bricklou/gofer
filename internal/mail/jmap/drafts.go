package jmap

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// DraftRevisionHeader is the header Gofer writes into each saved version of a
// draft, so a save whose outcome is unknown can be found on the server.
const DraftRevisionHeader = "X-Gofer-Draft-Revision"

// Draft is a draft email on the server.
type Draft struct {
	ID string
	// MessageID is the Message-ID header, in angle brackets.
	MessageID string
	// Revision is the DraftRevisionHeader value, if the draft has one.
	Revision string
}

// draftPage is how many drafts FindDrafts reads per request, within the
// server's limit; maxDrafts bounds how many it reads in all.
const (
	draftPage = 100
	maxDrafts = 5000
)

// FindDrafts lists the drafts in the Drafts mailbox whose Message-ID is
// messageID, with the revision each carries. Several versions of one draft
// can exist when an earlier save could not remove the version it replaced.
//
// It reads the mailbox and compares Message-IDs itself: servers need not
// support searching by header, and a search index may lag behind a draft
// that was just saved.
func (c *Client) FindDrafts(ctx context.Context, session *Session, accountID, draftsID, messageID string) ([]Draft, error) {
	bare := strings.Trim(strings.TrimSpace(messageID), "<>")
	if bare == "" || draftsID == "" {
		return nil, errors.New("jmap: finding a draft needs its Message-ID and the Drafts mailbox")
	}
	page := draftPage
	if limit := session.Core.MaxObjectsInGet; limit > 0 && limit < page {
		page = limit
	}
	revision := "header:" + DraftRevisionHeader + ":asText"
	var drafts []Draft
	for position := 0; position < maxDrafts; position += page {
		responses, err := c.Call(ctx, session, []string{CapabilityMail},
			Invocation{Name: "Email/query", Args: EmailQuery{AccountID: accountID, Filter: &EmailFilter{InMailbox: draftsID}, Position: position, Limit: page}, CallID: "0"},
			Invocation{Name: "Email/get", Args: map[string]any{
				"accountId":  accountID,
				"#ids":       ResultReference{ResultOf: "0", Name: "Email/query", Path: "/ids"},
				"properties": []string{"id", "messageId", revision},
			}, CallID: "1"},
		)
		if err != nil {
			return nil, err
		}
		var ids QueryResponse
		if err := responses.Get("0", &ids); err != nil {
			return nil, fmt.Errorf("Email/query: %w", err)
		}
		var found GetResponse[map[string]any]
		if err := responses.Get("1", &found); err != nil {
			return nil, fmt.Errorf("Email/get: %w", err)
		}
		for _, email := range found.List {
			messageIDs, _ := email["messageId"].([]any)
			if len(messageIDs) != 1 || messageIDs[0] != bare {
				continue
			}
			id, _ := email["id"].(string)
			text, _ := email[revision].(string)
			drafts = append(drafts, Draft{ID: id, MessageID: "<" + bare + ">", Revision: strings.TrimSpace(text)})
		}
		if len(ids.IDs) < page {
			return drafts, nil
		}
	}
	return nil, fmt.Errorf("jmap: the Drafts mailbox holds more than %d drafts", maxDrafts)
}

// DraftError is the server refusing to store a draft.
type DraftError struct {
	SetError
}

func (e *DraftError) Error() string { return "jmap: save draft: " + e.SetError.Error() }

// SaveDraft stores an uploaded message as a draft in the Drafts mailbox and
// removes the earlier versions in replace, in one request. Emails cannot be
// edited, so a new version replaces the old. It returns the new draft's id.
// A *DraftError means nothing was stored. When the draft was stored but an
// earlier version could not be removed, it returns the id and an error too.
func (c *Client) SaveDraft(ctx context.Context, session *Session, accountID, blobID, draftsID string, replace []string) (string, error) {
	if blobID == "" || draftsID == "" {
		return "", errors.New("jmap: saving a draft needs a blob and a Drafts mailbox")
	}
	calls := []Invocation{{Name: "Email/import", Args: map[string]any{
		"accountId": accountID,
		"emails": map[string]any{"draft": map[string]any{
			"blobId":     blobID,
			"mailboxIds": map[string]bool{draftsID: true},
			"keywords":   map[string]bool{KeywordDraft: true, KeywordSeen: true},
		}},
	}, CallID: "0"}}
	if len(replace) > 0 {
		calls = append(calls, Invocation{Name: "Email/set", Args: EmailSet{AccountID: accountID, Destroy: replace}, CallID: "1"})
	}
	responses, err := c.Call(ctx, session, []string{CapabilityMail}, calls...)
	if err != nil {
		return "", err
	}
	var imported struct {
		Created    map[string]struct{ ID string } `json:"created"`
		NotCreated map[string]SetError            `json:"notCreated"`
	}
	if err := responses.Get("0", &imported); err != nil {
		return "", fmt.Errorf("Email/import: %w", err)
	}
	if failure, ok := imported.NotCreated["draft"]; ok {
		return "", &DraftError{failure}
	}
	id := imported.Created["draft"].ID
	if id == "" {
		return "", errors.New("jmap: the server did not confirm the draft")
	}
	if len(replace) == 0 {
		return id, nil
	}
	var removed SetResponse
	if err := responses.Get("1", &removed); err != nil {
		return id, fmt.Errorf("remove earlier draft: %w", err)
	}
	for old, failure := range removed.NotDestroyed {
		if failure.Type != "notFound" {
			return id, fmt.Errorf("remove earlier draft %s: %w", old, &failure)
		}
	}
	return id, nil
}
