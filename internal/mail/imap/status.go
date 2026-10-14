package imap

import (
	"context"
	"fmt"

	"github.com/emersion/go-imap/v2"
)

// FolderStatus is what a server reports about a folder without selecting it.
// Comparing it with the local copy tells whether a poll has anything to do.
type FolderStatus struct {
	Messages    uint32
	UIDNext     uint32
	UIDValidity uint32
	Unseen      uint32
	// HighestModSeq changes with every flag change on servers with
	// CONDSTORE; zero when the server does not report it.
	HighestModSeq uint64
}

func (c *Client) statusOptions() *imap.StatusOptions {
	options := &imap.StatusOptions{NumMessages: true, UIDNext: true, UIDValidity: true, NumUnseen: true}
	if caps := c.client.Caps(); caps != nil && caps.Has(imap.CapCondStore) {
		options.HighestModSeq = true
	}
	return options
}

func (c *Client) supportsListStatus() bool {
	caps := c.client.Caps()
	return caps != nil && (caps.Has(imap.CapListStatus) || caps.Has(imap.CapIMAP4rev2))
}

// folderStatusFromData converts a STATUS response, reporting false when the
// server left out anything the comparison needs.
func folderStatusFromData(data *imap.StatusData) (FolderStatus, bool) {
	if data == nil || data.NumMessages == nil || data.NumUnseen == nil || data.UIDNext == 0 || data.UIDValidity == 0 {
		return FolderStatus{}, false
	}
	return FolderStatus{
		Messages:      *data.NumMessages,
		UIDNext:       uint32(data.UIDNext),
		UIDValidity:   data.UIDValidity,
		Unseen:        *data.NumUnseen,
		HighestModSeq: data.HighestModSeq,
	}, true
}

// FolderStatus asks the server about one folder with STATUS. The folder must
// not be selected.
func (c *Client) FolderStatus(ctx context.Context, remoteName string) (FolderStatus, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return FolderStatus{}, false, err
	}
	if c.closed {
		return FolderStatus{}, false, fmt.Errorf("client is closed")
	}
	data, err := c.client.Status(remoteName, c.statusOptions()).Wait()
	if err != nil {
		return FolderStatus{}, false, fmt.Errorf("status %s: %w", remoteName, err)
	}
	status, ok := folderStatusFromData(data)
	return status, ok, nil
}

func listStatusOptions(c *Client) *imap.ListOptions {
	if !c.supportsListStatus() {
		return nil
	}
	return &imap.ListOptions{ReturnStatus: c.statusOptions()}
}
