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
	if err := o.syncJMAPFolders(ctx, accountID, mailboxes.List); err != nil {
		return err
	}
	repo := o.outlookRepository()
	emailCursor, err := repo.GetLabelSyncState(ctx, accountID, storage.LabelProviderJMAP, jmapEmailScope)
	if err != nil {
		return err
	}
	if emailCursor.Cursor == "" {
		return o.importJMAPEmails(ctx, accountID, client, session, mailAccount, mailboxes)
	}
	err = o.syncJMAPEmailChanges(ctx, accountID, client, session, mailAccount, mailboxes, emailCursor.Cursor)
	if jmap.IsCannotCalculateChanges(err) {
		// The server no longer knows the saved state: import everything again.
		log.Printf("jmap sync %s: changes since %q are no longer available, importing again", accountID, emailCursor.Cursor)
		return o.importJMAPEmails(ctx, accountID, client, session, mailAccount, mailboxes)
	}
	if err != nil {
		return err
	}
	return repo.MarkLabelSyncSuccess(ctx, accountID, storage.LabelProviderJMAP, jmapMailboxScope, mailboxes.State, false)
}

// jmapMaxChanges is how many changed emails one Email/changes call returns.
const jmapMaxChanges = 500

// syncJMAPEmailChanges applies the emails created, updated and destroyed
// since the saved Email state. Each chunk is stored before its new state is
// saved, so an interrupted sync resumes from the last chunk stored. Created
// and updated emails are fetched as summaries, without bodies, and stored
// like imported ones, which also moves them between folders; an email no
// longer in any known mailbox, or gone, is removed from its folders.
func (o *SyncOrchestrator) syncJMAPEmailChanges(ctx context.Context, accountID string, client *jmap.Client, session *jmap.Session, mailAccount string, mailboxes *jmap.GetResponse[jmap.Mailbox], since string) error {
	folders := make(map[string]string, len(mailboxes.List))
	for _, mailbox := range mailboxes.List {
		folders[mailbox.ID] = jmapLocalFolderID(accountID, mailbox.ID)
	}
	batch := jmapImportPage
	if max := session.Core.MaxObjectsInGet; max > 0 && max < batch {
		batch = max
	}
	repo := o.outlookRepository()
	touched := map[string]bool{}
	for {
		changes, err := client.GetEmailChanges(ctx, session, mailAccount, since, jmapMaxChanges)
		if err != nil {
			return err
		}
		gone := append([]string(nil), changes.Destroyed...)
		changed := append(append([]string(nil), changes.Created...), changes.Updated...)
		for start := 0; start < len(changed); start += batch {
			end := min(start+batch, len(changed))
			got, err := client.GetEmails(ctx, session, mailAccount, changed[start:end], jmap.EmailSummaryProperties)
			if err != nil {
				return err
			}
			// Changed and then deleted before this request.
			gone = append(gone, got.NotFound...)
			var rows []storage.ProviderSyncMessage
			for _, email := range got.List {
				emailRows := jmapEmailToProviderSyncs(accountID, email, folders)
				if len(emailRows) == 0 {
					gone = append(gone, email.ID)
					continue
				}
				for _, row := range emailRows {
					rows = append(rows, row)
					touched[row.FolderID] = true
				}
			}
			if len(rows) > 0 {
				if _, err := repo.UpsertProviderSyncMessages(ctx, rows); err != nil {
					return err
				}
			}
		}
		for _, id := range gone {
			folderIDs, err := repo.MarkProviderMessageDeleted(ctx, accountID, id)
			if err != nil {
				return err
			}
			for _, folderID := range folderIDs {
				touched[folderID] = true
			}
		}
		if err := repo.MarkLabelSyncSuccess(ctx, accountID, storage.LabelProviderJMAP, jmapEmailScope, changes.NewState, false); err != nil {
			return err
		}
		if !changes.HasMoreChanges || changes.NewState == since {
			break
		}
		since = changes.NewState
	}
	for folderID := range touched {
		o.publishEvent(Event{Type: EventSyncComplete, AccountID: accountID, FolderID: folderID, Payload: map[string]any{"status": "complete", "provider": "jmap"}})
	}
	return nil
}

const (
	jmapEmailScope   = "email"
	jmapMailboxScope = "mailbox"
	// jmapImportPage is how many emails one import request fetches, within the
	// server's own limit.
	jmapImportPage = 100
)

// importJMAPEmails stores every email of the account, newest first, removes
// from each folder the messages the server no longer has in it, then saves the
// Email and Mailbox states as the account's sync cursors. The Email
// state is read before the first page, so mail that arrives during the import
// is caught by the changes since it. An interrupted import saves no cursor
// and simply runs again; storing an email twice only updates it.
func (o *SyncOrchestrator) importJMAPEmails(ctx context.Context, accountID string, client *jmap.Client, session *jmap.Session, mailAccount string, mailboxes *jmap.GetResponse[jmap.Mailbox]) error {
	before, err := client.GetEmails(ctx, session, mailAccount, []string{}, nil)
	if err != nil {
		return err
	}
	folders := make(map[string]string, len(mailboxes.List))
	for _, mailbox := range mailboxes.List {
		folders[mailbox.ID] = jmapLocalFolderID(accountID, mailbox.ID)
	}
	limit := jmapImportPage
	if max := session.Core.MaxObjectsInGet; max > 0 && max < limit {
		limit = max
	}
	repo := o.outlookRepository()
	touched := map[string]bool{}
	seen := map[string]map[string]bool{}
	anchor := ""
	for {
		query := jmap.EmailQuery{
			AccountID: mailAccount,
			Sort:      []jmap.Comparator{{Property: "receivedAt", IsAscending: false}},
			Limit:     limit,
		}
		if anchor != "" {
			query.Anchor, query.AnchorOffset = anchor, 1
		}
		page, emails, err := client.QueryAndGetEmails(ctx, session, query, jmap.EmailSummaryProperties)
		if jmap.IsAnchorNotFound(err) {
			// The email the page started from was deleted; start over.
			anchor = ""
			continue
		}
		if err != nil {
			return err
		}
		var rows []storage.ProviderSyncMessage
		for _, email := range emails.List {
			for _, row := range jmapEmailToProviderSyncs(accountID, email, folders) {
				rows = append(rows, row)
				touched[row.FolderID] = true
				if seen[row.FolderID] == nil {
					seen[row.FolderID] = map[string]bool{}
				}
				seen[row.FolderID][row.ProviderMessageID] = true
			}
		}
		if len(rows) > 0 {
			if _, err := repo.UpsertProviderSyncMessages(ctx, rows); err != nil {
				return err
			}
		}
		// A server may cap the page below the limit asked for; a page shorter
		// than its own limit is the last one.
		pageLimit := limit
		if page.Limit > 0 && page.Limit < pageLimit {
			pageLimit = page.Limit
		}
		if len(page.IDs) == 0 || len(page.IDs) < pageLimit {
			break
		}
		anchor = page.IDs[len(page.IDs)-1]
	}
	// After a new import of a known account, as when the server could no
	// longer give the changes since its cursor, messages deleted meanwhile are
	// still stored.
	for _, folderID := range folders {
		removed, err := repo.MarkProviderMessagesMissingFromFolder(ctx, accountID, folderID, seen[folderID])
		if err != nil {
			return err
		}
		if removed > 0 {
			touched[folderID] = true
		}
	}
	if err := repo.MarkLabelSyncSuccess(ctx, accountID, storage.LabelProviderJMAP, jmapEmailScope, before.State, true); err != nil {
		return err
	}
	if err := repo.MarkLabelSyncSuccess(ctx, accountID, storage.LabelProviderJMAP, jmapMailboxScope, mailboxes.State, true); err != nil {
		return err
	}
	for folderID := range touched {
		o.publishEvent(Event{Type: EventSyncComplete, AccountID: accountID, FolderID: folderID, Payload: map[string]any{"status": "complete", "provider": "jmap"}})
	}
	return nil
}

// jmapEmailToProviderSyncs gives one row per known mailbox the email is in,
// which together set the folders the email shows in. Keywords carry the read,
// flagged and draft states.
func jmapEmailToProviderSyncs(accountID string, email jmap.Email, folders map[string]string) []storage.ProviderSyncMessage {
	id := strings.TrimSpace(email.ID)
	if id == "" {
		return nil
	}
	internetMessageID := ""
	if len(email.MessageID) > 0 {
		internetMessageID = "<" + strings.Trim(email.MessageID[0], "<>") + ">"
	}
	var fromName, fromEmail string
	if len(email.From) > 0 {
		fromName, fromEmail = email.From[0].Name, email.From[0].Email
	}
	flagged := email.Keywords[jmap.KeywordFlagged]
	base := storage.ProviderSyncMessage{
		AccountID:         accountID,
		ProviderMessageID: id,
		InternetMessageID: internetMessageID,
		ProviderThreadID:  strings.TrimSpace(email.ThreadID),
		InReplyTo:         jmapMessageIDList(email.InReplyTo),
		References:        jmapMessageIDList(email.References),
		Subject:           strings.TrimSpace(email.Subject),
		FromName:          fromName,
		FromEmail:         fromEmail,
		DateSent:          email.SentAt,
		DateReceived:      email.ReceivedAt,
		Snippet:           strings.TrimSpace(email.Preview),
		IsRead:            email.Keywords[jmap.KeywordSeen],
		IsStarred:         flagged,
		IsFlagged:         flagged,
		IsDraft:           email.Keywords[jmap.KeywordDraft],
		HasAttachments:    email.HasAttachment,
		SizeBytes:         email.Size,
		ToRecipients:      jmapRecipients(email.To),
		CCRecipients:      jmapRecipients(email.CC),
		BCCRecipients:     jmapRecipients(email.BCC),
	}
	var rows []storage.ProviderSyncMessage
	for mailboxID, in := range email.MailboxIDs {
		folderID, known := folders[mailboxID]
		if !in || !known {
			continue
		}
		row := base
		row.FolderID = folderID
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].FolderID < rows[j].FolderID })
	return rows
}

// jmapMessageIDList writes JMAP's bare message ids as a header value.
func jmapMessageIDList(ids []string) string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id = strings.Trim(strings.TrimSpace(id), "<>"); id != "" {
			out = append(out, "<"+id+">")
		}
	}
	return strings.Join(out, " ")
}

func jmapRecipients(addresses []jmap.EmailAddress) []storage.Recipient {
	out := make([]storage.Recipient, 0, len(addresses))
	for _, address := range addresses {
		if strings.TrimSpace(address.Email) != "" {
			out = append(out, storage.Recipient{Name: address.Name, Email: address.Email})
		}
	}
	return out
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
