package imap

import (
	"context"
	"errors"
	"fmt"

	goimap "github.com/emersion/go-imap/v2"
)

// Delimiter returns the character the server separates folder levels with,
// or 0 when it has only one level.
func (c *Client) Delimiter(ctx context.Context) (rune, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if c.closed {
		return 0, fmt.Errorf("client is closed")
	}
	data, err := c.client.List("", "", nil).Collect()
	if err != nil {
		return 0, fmt.Errorf("list delimiter: %w", err)
	}
	for _, mailbox := range data {
		if mailbox.Delim != 0 {
			return mailbox.Delim, nil
		}
	}
	return 0, nil
}

// CreateMailbox creates a folder. A folder that already exists is left as
// it is.
func (c *Client) CreateMailbox(ctx context.Context, name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.closed {
		return fmt.Errorf("client is closed")
	}
	if name == "" {
		return fmt.Errorf("mailbox is required")
	}
	err := c.client.Create(name, nil).Wait()
	if err == nil {
		return nil
	}
	var statusErr *goimap.Error
	if !errors.As(err, &statusErr) {
		return fmt.Errorf("create %s: %w", name, err)
	}
	// Not every server says ALREADYEXISTS, so look for the folder.
	existing, listErr := c.client.List("", name, nil).Collect()
	if listErr == nil && len(existing) > 0 {
		return nil
	}
	return fmt.Errorf("create %s: %w", name, err)
}
