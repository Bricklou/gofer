package jmap

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// PatchObject changes parts of an object in a /set update (RFC 8620 section
// 5.3): each key is a JSON pointer path, and a nil value removes that path.
type PatchObject map[string]any

// KeywordPatch sets or clears one keyword, leaving the email's others alone.
func KeywordPatch(keyword string, on bool) PatchObject {
	var value any
	if on {
		value = true
	}
	return PatchObject{"keywords/" + escapePointer(keyword): value}
}

// MovePatch moves an email out of one mailbox into another, leaving any other
// mailboxes it is in alone.
func MovePatch(from, to string) PatchObject {
	patch := PatchObject{"mailboxIds/" + escapePointer(to): true}
	if from != "" && from != to {
		patch["mailboxIds/"+escapePointer(from)] = nil
	}
	return patch
}

// escapePointer escapes one JSON pointer path segment (RFC 6901).
func escapePointer(segment string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(segment)
}

// SetError is why the server did not create, update or destroy one object.
type SetError struct {
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Properties  []string `json:"properties"`
}

func (e *SetError) Error() string {
	if e.Description == "" {
		return "jmap: " + e.Type
	}
	return fmt.Sprintf("jmap: %s: %s", e.Type, e.Description)
}

// EmailSet are the Email/set arguments Gofer uses: updates by id and ids to
// destroy for good.
type EmailSet struct {
	AccountID string                 `json:"accountId"`
	Update    map[string]PatchObject `json:"update,omitempty"`
	Destroy   []string               `json:"destroy,omitempty"`
}

// SetResponse is the standard /set result. Each id asked about ends up either
// in Updated or Destroyed, or in NotUpdated or NotDestroyed with the reason.
type SetResponse struct {
	AccountID    string                     `json:"accountId"`
	OldState     string                     `json:"oldState"`
	NewState     string                     `json:"newState"`
	Updated      map[string]json.RawMessage `json:"updated"`
	Destroyed    []string                   `json:"destroyed"`
	NotUpdated   map[string]SetError        `json:"notUpdated"`
	NotDestroyed map[string]SetError        `json:"notDestroyed"`
}

// SetEmails runs Email/set.
func (c *Client) SetEmails(ctx context.Context, session *Session, args EmailSet) (*SetResponse, error) {
	responses, err := c.Call(ctx, session, []string{CapabilityMail},
		Invocation{Name: "Email/set", Args: args, CallID: "0"})
	if err != nil {
		return nil, err
	}
	var result SetResponse
	if err := responses.Get("0", &result); err != nil {
		return nil, fmt.Errorf("Email/set: %w", err)
	}
	return &result, nil
}
