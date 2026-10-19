package mail

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/cristianadrielbraun/gofer/internal/mail/jmap"
	"github.com/cristianadrielbraun/gofer/internal/models"
	"github.com/cristianadrielbraun/gofer/internal/providers"
)

// JMAPImport adds messages to a JMAP account's mailboxes, for imports and
// for mail moved in from another account. It signs in on first use and keeps
// the session until a request fails.
type JMAPImport struct {
	config   *models.AccountConfig
	password string

	client    *jmap.Client
	session   *jmap.Session
	account   string
	mailboxes []jmap.Mailbox
}

func NewJMAPImport(cfg *models.AccountConfig, password string) *JMAPImport {
	return &JMAPImport{config: cfg, password: password}
}

func (j *JMAPImport) open(ctx context.Context) error {
	if j.session != nil {
		return nil
	}
	if !providers.JMAPEnabled() {
		return providers.ErrJMAPDisabled
	}
	client, session, account, err := openJMAPMail(ctx, j.config, j.password)
	if err != nil {
		return err
	}
	j.client, j.session, j.account, j.mailboxes = client, session, account, nil
	return nil
}

// drop forgets the session after a failed request, so the next one signs in
// again. A refusal leaves it: the server answered.
func (j *JMAPImport) drop(err error) {
	var refused *jmap.ImportError
	if err != nil && !errors.As(err, &refused) {
		j.session = nil
	}
}

// EnsureMailbox returns the id of the mailbox at path, its names separated
// by "/", creating the mailboxes along it that don't exist yet.
func (j *JMAPImport) EnsureMailbox(ctx context.Context, path string) (id string, err error) {
	defer func() { j.drop(err) }()
	if err := j.open(ctx); err != nil {
		return "", err
	}
	if j.mailboxes == nil {
		mailboxes, err := j.client.GetMailboxes(ctx, j.session, j.account, nil)
		if err != nil {
			return "", err
		}
		j.mailboxes = mailboxes.List
	}
	parent := ""
	for _, name := range strings.Split(strings.Trim(path, "/"), "/") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		found := ""
		for _, mailbox := range j.mailboxes {
			if mailbox.ParentID != parent {
				continue
			}
			// "INBOX" names the inbox whatever the server calls it.
			if mailbox.Name == name || (parent == "" && strings.EqualFold(name, "inbox") && mailbox.Role == "inbox") {
				found = mailbox.ID
				break
			}
		}
		if found == "" {
			found, err = j.client.CreateMailbox(ctx, j.session, j.account, name, parent)
			if err != nil {
				return "", err
			}
			j.mailboxes = append(j.mailboxes, jmap.Mailbox{ID: found, Name: name, ParentID: parent})
		}
		parent = found
	}
	if parent == "" {
		return "", errors.New("the folder needs a name")
	}
	return parent, nil
}

// Import uploads raw and stores it in the mailbox with the keywords given,
// received at receivedAt when it isn't zero. A message the server already
// has counts as imported.
func (j *JMAPImport) Import(ctx context.Context, mailboxID string, raw []byte, keywords []string, receivedAt time.Time) (err error) {
	defer func() { j.drop(err) }()
	if err := j.open(ctx); err != nil {
		return err
	}
	blob, err := j.client.UploadBlob(ctx, j.session, j.account, jmap.ContentTypeMessage, raw)
	if err != nil {
		if errors.Is(err, jmap.ErrUploadTooLarge) {
			// The session is fine; the message is what's wrong.
			return &jmap.ImportError{Step: "upload", SetError: jmap.SetError{Type: "tooLarge", Description: err.Error()}}
		}
		return err
	}
	_, err = j.client.ImportEmail(ctx, j.session, j.account, blob, mailboxID, keywords, receivedAt)
	return err
}
