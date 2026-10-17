package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	goimap "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

type mailImportUploadFile struct {
	name string
	data []byte
}

func mailImportUploadReader(t *testing.T, files ...mailImportUploadFile) *multipart.Reader {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, file := range files {
		part, err := writer.CreateFormFile("files", file.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(file.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return multipart.NewReader(&body, writer.Boundary())
}

func (f *mailExportFixture) waitImportFinished(t *testing.T, id string) *mailImportJob {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		job, err := f.h.mailImports.load(id)
		if err != nil {
			t.Fatal(err)
		}
		if job.State != mailJobRunning && !f.h.mailImports.isRunning(id) {
			return job
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("import did not finish")
	return nil
}

type serverMessage struct {
	flags []goimap.Flag
	raw   string
}

func (f *mailExportFixture) serverMessages(t *testing.T, mailbox string) []serverMessage {
	t.Helper()
	client, err := imapclient.DialInsecure(f.imapAddr, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Login("robin@example.test", "mailbox-secret").Wait(); err != nil {
		t.Fatal(err)
	}
	selected, err := client.Select(mailbox, nil).Wait()
	if err != nil {
		t.Fatalf("select %s: %v", mailbox, err)
	}
	if selected.NumMessages == 0 {
		return nil
	}
	section := &goimap.FetchItemBodySection{Peek: true}
	var all goimap.SeqSet
	all.AddRange(1, selected.NumMessages)
	fetched, err := client.Fetch(all, &goimap.FetchOptions{Flags: true, BodySection: []*goimap.FetchItemBodySection{section}}).Collect()
	if err != nil {
		t.Fatal(err)
	}
	var messages []serverMessage
	for _, message := range fetched {
		messages = append(messages, serverMessage{flags: message.Flags, raw: string(message.FindBodySection(section))})
	}
	return messages
}

func hasFlag(flags []goimap.Flag, flag goimap.Flag) bool {
	for _, f := range flags {
		if f == flag {
			return true
		}
	}
	return false
}

// A Gofer export imported again goes into the folders the review suggests or
// the person picks, keeps each message's state, and skips messages already
// in the folder.
func TestMailImportOfAGoferExport(t *testing.T) {
	f := newMailExportFixture(t)
	export, err := f.h.createMailExport(t.Context(), "owner", f.account, []string{f.folders["inbox"], f.folders["archive-2024"]}, false)
	if err != nil {
		t.Fatal(err)
	}
	f.waitFinished(t, export.ID)
	rec, _ := f.download(t, export.ID, ownerRequest)

	job, err := f.h.saveMailImportUpload("owner", mailImportUploadReader(t, mailImportUploadFile{"gofer-mail.zip", rec.Body.Bytes()}))
	if err != nil {
		t.Fatal(err)
	}
	if job.ExportedFrom != "robin@example.test" || len(job.Sources) != 2 {
		t.Fatalf("upload = %+v", job)
	}
	inbox, archive := job.Sources[0], job.Sources[1]
	if inbox.Role != "inbox" || inbox.Messages != 3 || archive.Path != "Archive/2024" || archive.Messages != 1 {
		t.Fatalf("sources = %+v, %+v", inbox, archive)
	}

	req := ownerRequest(httptest.NewRequest(http.MethodGet, "/api/mail/imports/"+job.ID+"/review", nil))
	req.SetPathValue("id", job.ID)
	review := httptest.NewRecorder()
	f.h.handleReviewMailImport(review, req)
	body := review.Body.String()
	for _, want := range []string{
		`data-tui-selectbox-value="` + f.folders["inbox"] + `" data-tui-selectbox-selected="true"`,
		`data-tui-selectbox-value="` + f.folders["archive-2024"] + `" data-tui-selectbox-selected="true"`,
		"Exported from robin@example.test by Gofer.",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("review lacks %q:\n%s", want, body)
		}
	}
	if attacker := attackerRequest(httptest.NewRequest(http.MethodGet, "/api/mail/imports/"+job.ID+"/review", nil)); true {
		attacker.SetPathValue("id", job.ID)
		rec := httptest.NewRecorder()
		f.h.handleReviewMailImport(rec, attacker)
		if strings.Contains(rec.Body.String(), "data-mail-import-review") {
			t.Fatal("another person can review the upload")
		}
	}

	form := url.Values{
		"account":  {f.account.ID},
		"target:0": {mailImportTargetNew}, "new:0": {"Restored/2025"},
		"target:1": {f.folders["archive-2024"]},
	}
	start := ownerRequest(httptest.NewRequest(http.MethodPost, "/api/mail/imports/"+job.ID+"/start", strings.NewReader(form.Encode())))
	start.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	start.SetPathValue("id", job.ID)
	started := httptest.NewRecorder()
	f.h.handleStartMailImport(started, start)
	if !strings.Contains(started.Header().Get("HX-Trigger"), `"gofer-close-dialog":"mail-import-dialog"`) || !strings.Contains(started.Body.String(), "data-mail-import-upload") {
		t.Fatalf("start = %s", started.Body.String())
	}
	job = f.waitImportFinished(t, job.ID)
	if job.State != mailJobDone {
		t.Fatalf("state = %s (%s)", job.State, job.Error)
	}
	_, _, imported, duplicates, failed := job.totals()
	if imported != 3 || duplicates != 1 || failed != 0 {
		t.Fatalf("imported %d, duplicates %d, failed %d", imported, duplicates, failed)
	}

	restored := f.serverMessages(t, "Restored/2025")
	if len(restored) != 3 {
		t.Fatalf("Restored/2025 has %d messages", len(restored))
	}
	state := map[string][]goimap.Flag{}
	for _, message := range restored {
		if strings.Contains(message.raw, "Status:") {
			t.Fatalf("uploaded message kept the mbox state headers:\n%s", message.raw)
		}
		for _, subject := range []string{"Garden plans", "Quoted line", "Saved copy"} {
			if strings.Contains(message.raw, "Subject: "+subject+"\r\n") {
				state[subject] = message.flags
			}
		}
	}
	if !hasFlag(state["Garden plans"], goimap.FlagSeen) || hasFlag(state["Quoted line"], goimap.FlagSeen) || !hasFlag(state["Quoted line"], goimap.FlagFlagged) {
		t.Fatalf("flags = %v", state)
	}
	for _, message := range restored {
		if strings.Contains(message.raw, "Quoted line") && !strings.Contains(message.raw, "\r\nFrom the desk of Juno:\r\n>From an older note.") {
			t.Fatalf("quoted lines were not restored:\n%s", message.raw)
		}
	}
	if archived := f.serverMessages(t, "Archive/2024"); len(archived) != 1 {
		t.Fatalf("Archive/2024 has %d messages, the duplicate was uploaded", len(archived))
	}

	views, err := f.h.mailImportJobViews("attacker")
	if err != nil || len(views) != 0 {
		t.Fatalf("another person sees %d imports (%v)", len(views), err)
	}
	for _, action := range []string{"resume", "delete"} {
		req := attackerRequest(httptest.NewRequest(http.MethodPost, "/api/mail/imports/"+job.ID+"/"+action, nil))
		req.SetPathValue("id", job.ID)
		rec := httptest.NewRecorder()
		map[string]http.HandlerFunc{"resume": f.h.handleResumeMailImport, "delete": f.h.handleDeleteMailImport}[action](rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s by another person = %d", action, rec.Code)
		}
	}
}

func TestMailImportFindsMessagesInLooseFiles(t *testing.T) {
	f := newMailExportFixture(t)
	mbox := "From juno@example.test Mon Mar  3 10:00:00 2025\nSubject: One\n\nBody\n\nFrom juno@example.test Mon Mar  3 11:00:00 2025\nSubject: Two\n\nBody\n"
	job, err := f.h.saveMailImportUpload("owner", mailImportUploadReader(t,
		mailImportUploadFile{"Sent Items.mbox", []byte(mbox)},
		mailImportUploadFile{"first.eml", []byte(mailExportTestMessage("Loose one", "Hello."))},
		mailImportUploadFile{"second.EML", []byte(mailExportTestMessage("Loose two", "Hello again."))},
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(job.Sources) != 2 || job.Sources[0].Role != "sent" || job.Sources[0].Messages != 2 || job.Sources[1].Kind != mailImportKindEML || job.Sources[1].Messages != 2 {
		t.Fatalf("sources = %+v %+v", job.Sources[0], job.Sources[1])
	}
	if _, err := f.h.saveMailImportUpload("owner", mailImportUploadReader(t, mailImportUploadFile{"notes.mbox", nil})); err != errMailImportNoMessages {
		t.Fatalf("empty upload = %v", err)
	}

	form := url.Values{"target:0": {mailImportTargetSkip}, "target:1": {f.folders["inbox"]}}
	if err := f.h.startMailImport(t.Context(), job, f.account, form); err != nil {
		t.Fatal(err)
	}
	job = f.waitImportFinished(t, job.ID)
	_, total, imported, _, _ := job.totals()
	if job.State != mailJobDone || imported != 2 || total != 2 {
		t.Fatalf("state %s, imported %d of %d", job.State, imported, total)
	}
	if inbox := f.serverMessages(t, "INBOX"); len(inbox) != 5 {
		t.Fatalf("INBOX has %d messages, want the 3 it had and 2 imported", len(inbox))
	}
}

// Gmail gets each message through messages.import, with the label of the
// chosen folder and the message's state as labels.
func TestGmailImportWriter(t *testing.T) {
	var mu sync.Mutex
	var labelNames []string
	var imports []struct {
		query    url.Values
		labels   []string
		message  string
		token    string
		boundary bool
	}
	unauthorized := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/gmail/v1/users/me/labels":
			_, _ = io.WriteString(w, `{"labels":[{"id":"INBOX","name":"INBOX"},{"id":"Label_7","name":"Projects"}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/gmail/v1/users/me/labels":
			var label struct{ Name string }
			_ = json.NewDecoder(r.Body).Decode(&label)
			labelNames = append(labelNames, label.Name)
			_, _ = io.WriteString(w, `{"id":"Label_9"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/upload/gmail/v1/users/me/messages/import":
			if unauthorized {
				unauthorized = false
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			mediaType, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
			reader := multipart.NewReader(r.Body, params["boundary"])
			metadata, _ := reader.NextPart()
			var meta struct {
				LabelIDs []string `json:"labelIds"`
			}
			_ = json.NewDecoder(metadata).Decode(&meta)
			message, _ := reader.NextPart()
			raw, _ := io.ReadAll(message)
			imports = append(imports, struct {
				query    url.Values
				labels   []string
				message  string
				token    string
				boundary bool
			}{r.URL.Query(), meta.LabelIDs, string(raw), r.Header.Get("Authorization"), mediaType == "multipart/related"})
			_, _ = io.WriteString(w, `{"id":"m1"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	previous := gmailAPIBaseURL
	gmailAPIBaseURL = server.URL + "/gmail/v1"
	t.Cleanup(func() { gmailAPIBaseURL = previous })

	refreshed := 0
	writer := &gmailImportWriter{token: func(_ context.Context, refresh bool) (string, error) {
		if refresh {
			refreshed++
			return "fresh-token", nil
		}
		return "old-token", nil
	}}
	ctx := t.Context()
	if label, err := writer.ensureFolder(ctx, "projects"); err != nil || label != "Label_7" {
		t.Fatalf("existing label = %q, %v", label, err)
	}
	if label, err := writer.ensureFolder(ctx, "Projects/Garden"); err != nil || label != "Label_9" || len(labelNames) != 1 || labelNames[0] != "Projects/Garden" {
		t.Fatalf("new label = %q, %v, created %v", label, err, labelNames)
	}
	msg := prepareMailImportMessage([]byte(mailExportTestMessage("Garden plans", "The tomatoes go by the fence.")), "")
	msg.Flagged = true
	if err := writer.upload(ctx, "Label_9", msg); err != nil {
		t.Fatal(err)
	}
	msg.Read = true
	msg.Flagged = false
	if err := writer.upload(ctx, "ARCHIVE", msg); err != nil {
		t.Fatal(err)
	}
	if len(imports) != 2 || refreshed != 1 {
		t.Fatalf("imports %d, token refreshed %d times", len(imports), refreshed)
	}
	first := imports[0]
	if !first.boundary || first.token != "Bearer fresh-token" || first.query.Get("uploadType") != "multipart" || first.query.Get("internalDateSource") != "dateHeader" || first.query.Get("neverMarkSpam") != "true" {
		t.Fatalf("request = %+v", first)
	}
	if strings.Join(first.labels, ",") != "Label_9,UNREAD,STARRED" || !strings.Contains(first.message, "Subject: Garden plans") {
		t.Fatalf("first import labels %v, message %q", first.labels, first.message)
	}
	if len(imports[1].labels) != 0 {
		t.Fatalf("archived message labels = %v", imports[1].labels)
	}
}

// An import stopped partway through an mbox file continues after the last
// message it recorded.
func TestMailImportContinuesWhereItStopped(t *testing.T) {
	f := newMailExportFixture(t)
	mbox := "From juno@example.test Mon Mar  3 10:00:00 2025\nSubject: Part one\nMessage-ID: <part-one@example.test>\n\nBody\n\n" +
		"From juno@example.test Mon Mar  3 11:00:00 2025\nSubject: Part two\nMessage-ID: <part-two@example.test>\n\nBody\n\n" +
		"From juno@example.test Mon Mar  3 12:00:00 2025\nSubject: Part three\nMessage-ID: <part-three@example.test>\n\nBody\n"
	job, err := f.h.saveMailImportUpload("owner", mailImportUploadReader(t, mailImportUploadFile{"Parts.mbox", []byte(mbox)}))
	if err != nil {
		t.Fatal(err)
	}
	first, err := newMboxReader(strings.NewReader(mbox), 0).Next(false)
	if err != nil {
		t.Fatal(err)
	}
	source := job.Sources[0]
	source.Target, source.TargetFolderID, source.TargetRemote, source.TargetName = f.folders["inbox"], f.folders["inbox"], "INBOX", "Inbox"
	source.Done, source.Imported, source.Offset = 1, 1, first.End
	job.AccountID, job.AccountEmail, job.Provider, job.State = f.account.ID, f.account.Email, "imap", mailJobPaused
	if err := f.h.mailImports.save(job); err != nil {
		t.Fatal(err)
	}
	req := ownerRequest(httptest.NewRequest(http.MethodPost, "/api/mail/imports/"+job.ID+"/resume", nil))
	req.SetPathValue("id", job.ID)
	f.h.handleResumeMailImport(httptest.NewRecorder(), req)
	job = f.waitImportFinished(t, job.ID)
	_, _, imported, _, _ := job.totals()
	if job.State != mailJobDone || imported != 3 {
		t.Fatalf("state %s, imported %d", job.State, imported)
	}
	var subjects []string
	for _, message := range f.serverMessages(t, "INBOX")[3:] {
		for _, line := range strings.Split(message.raw, "\n") {
			if strings.HasPrefix(line, "Subject: ") {
				subjects = append(subjects, strings.TrimSpace(strings.TrimPrefix(line, "Subject: ")))
			}
		}
	}
	if strings.Join(subjects, ",") != "Part two,Part three" {
		t.Fatalf("uploaded after continuing: %v", subjects)
	}
}
