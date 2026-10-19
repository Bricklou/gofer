package mail

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/cristianadrielbraun/gofer/internal/mail/imap"
	"github.com/cristianadrielbraun/gofer/internal/mail/jmap"
	"github.com/cristianadrielbraun/gofer/internal/models"
	"github.com/cristianadrielbraun/gofer/internal/storage"
)

// syncJMAPAccount syncs a JMAP account. JMAP accounts share the provider
// repository that Gmail and Outlook use.
func (o *SyncOrchestrator) syncJMAPAccount(ctx context.Context, accountID string, cfg *models.AccountConfig) error {
	password, err := o.resolvePassword(ctx, cfg, accountID)
	if err != nil {
		return err
	}
	client, err := newJMAPAccountClient(cfg, password)
	if err != nil {
		return err
	}
	session, err := client.Session(ctx, cfg.JMAPSessionURL)
	if err != nil {
		return fmt.Errorf("JMAP session: %w", err)
	}
	mailAccount := session.PrimaryAccount(jmap.CapabilityMail)
	if mailAccount == "" {
		return fmt.Errorf("the JMAP server offers no mail account for this login")
	}
	mailboxes, err := client.GetMailboxes(ctx, session, mailAccount, nil)
	if err != nil {
		return err
	}
	return o.syncJMAPFolders(ctx, accountID, mailboxes.List)
}

// syncJMAPFolders stores the account's mailboxes as folders. A mailbox keeps
// its folder through renames and moves, since folders are matched by mailbox
// id; mailboxes the server no longer lists go through the usual folder
// reconciliation.
func (o *SyncOrchestrator) syncJMAPFolders(ctx context.Context, accountID string, mailboxes []jmap.Mailbox) error {
	byID := make(map[string]jmap.Mailbox, len(mailboxes))
	for _, mailbox := range mailboxes {
		if strings.TrimSpace(mailbox.ID) != "" {
			byID[mailbox.ID] = mailbox
		}
	}
	ordered := make([]jmap.Mailbox, 0, len(byID))
	for _, mailbox := range byID {
		ordered = append(ordered, mailbox)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		ri, rj := jmapFolderSortOrder(jmapFolderRole(ordered[i]), 0), jmapFolderSortOrder(jmapFolderRole(ordered[j]), 0)
		if ri != rj {
			return ri < rj
		}
		if ordered[i].SortOrder != ordered[j].SortOrder {
			return ordered[i].SortOrder < ordered[j].SortOrder
		}
		return strings.ToLower(jmapMailboxPath(ordered[i], byID)) < strings.ToLower(jmapMailboxPath(ordered[j], byID))
	})

	inputs := make([]storage.UpsertFolderInput, 0, len(ordered))
	seen := make([]string, 0, len(ordered))
	for i, mailbox := range ordered {
		role := jmapFolderRole(mailbox)
		parentID := ""
		if _, ok := byID[mailbox.ParentID]; ok {
			parentID = jmapLocalFolderID(accountID, mailbox.ParentID)
		}
		path := jmapMailboxPath(mailbox, byID)
		inputs = append(inputs, storage.UpsertFolderInput{
			ID:               jmapLocalFolderID(accountID, mailbox.ID),
			AccountID:        accountID,
			ParentID:         parentID,
			RemoteID:         path,
			ProviderRemoteID: mailbox.ID,
			Name:             jmapFolderName(mailbox, role),
			Icon:             imap.RoleIcon(role),
			Role:             role,
			Selectable:       mailbox.MyRights.MayReadItems,
			SortOrder:        jmapFolderSortOrder(role, i),
			CountsKnown:      true,
			TotalCount:       mailbox.TotalEmails,
			UnreadCount:      mailbox.UnreadEmails,
		})
		seen = append(seen, mailbox.ID)
	}
	repo := o.outlookRepository()
	if len(inputs) > 0 {
		if err := repo.UpsertFolders(ctx, inputs); err != nil {
			return err
		}
	}
	if err := repo.MarkUnlistedProviderFoldersNonSelectable(ctx, accountID, providerRemoteIDsFromFolderInputs(inputs)); err != nil {
		return err
	}
	result, err := repo.ReconcileDiscoveredFolders(ctx, accountID, storage.FolderDiscoveryJMAP, seen, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("reconcile JMAP folders: %w", err)
	}
	if result.Changed() {
		log.Printf("reconciled JMAP folders %s: missing=%d removed=%d recovered=%d", accountID, len(result.MissingIDs), len(result.RemovedIDs), len(result.RecoveredIDs))
		o.publishFolderReconciliationChange(accountID, result)
	}
	return nil
}

func jmapLocalFolderID(accountID, mailboxID string) string {
	return storage.FolderIDForIdentity(accountID, "jmap", mailboxID)
}

// jmapFolderRole maps a mailbox role (RFC 8621 section 2, from the IANA IMAP
// mailbox name attributes) to Gofer's folder roles. Roles Gofer has no folder
// kind for, such as "all" or "important", are custom folders.
func jmapFolderRole(mailbox jmap.Mailbox) string {
	switch strings.ToLower(strings.TrimSpace(mailbox.Role)) {
	case "inbox":
		return "inbox"
	case "sent":
		return "sent"
	case "drafts":
		return "drafts"
	case "trash":
		return "trash"
	case "junk":
		return "junk"
	case "archive":
		return "archive"
	default:
		return "custom"
	}
}

// jmapFolderName shows special mailboxes with Gofer's usual names, whatever
// the server calls them, and others with their own name.
func jmapFolderName(mailbox jmap.Mailbox, role string) string {
	if role == "custom" {
		return strings.TrimSpace(mailbox.Name)
	}
	return displayName(mailbox.Name, role)
}

// jmapMailboxPath is the mailbox's name under its parents, such as
// "Projects/2026". A parent loop the server should never send is cut short.
func jmapMailboxPath(mailbox jmap.Mailbox, byID map[string]jmap.Mailbox) string {
	parts := []string{strings.TrimSpace(mailbox.Name)}
	seen := map[string]bool{mailbox.ID: true}
	for parent, ok := byID[mailbox.ParentID]; ok && !seen[parent.ID]; parent, ok = byID[parent.ParentID] {
		seen[parent.ID] = true
		parts = append([]string{strings.TrimSpace(parent.Name)}, parts...)
	}
	return strings.Join(parts, "/")
}

func jmapFolderSortOrder(role string, fallback int) int {
	return outlookGraphFolderSortOrder(role, fallback)
}
