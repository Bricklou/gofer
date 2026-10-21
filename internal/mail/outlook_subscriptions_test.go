package mail

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cristianadrielbraun/gofer/internal/providers"
)

const testOutlookWebhookURL = "https://mail.example.com/webhooks/outlook"

var testOutlookWebhookKey = []byte("0123456789abcdef0123456789abcdef")

func TestOutlookClientStateNamesAndSignsTheAccount(t *testing.T) {
	state, err := outlookClientState(testOutlookWebhookKey, "acc-1")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := outlookClientStateAccount(testOutlookWebhookKey, state); !ok || got != "acc-1" {
		t.Fatalf("account of %q = %q %t", state, got, ok)
	}
	signature := state[strings.LastIndexByte(state, '.'):]
	for _, forged := range []string{
		"acc-2" + signature,
		state + "x",
		"acc-1",
		"",
		"." + signature,
	} {
		if got, ok := outlookClientStateAccount(testOutlookWebhookKey, forged); ok {
			t.Fatalf("forged state %q accepted for %q", forged, got)
		}
	}
	if _, ok := outlookClientStateAccount([]byte("another key of thirty-two bytes!"), state); ok {
		t.Fatal("state accepted under another key")
	}
	if _, err := outlookClientState(testOutlookWebhookKey, strings.Repeat("a", 100)); err == nil {
		t.Fatal("accepted a client state longer than Graph allows")
	}
}

// fakeOutlookSubscriptionGraph answers the subscription calls Gofer makes.
type fakeOutlookSubscriptionGraph struct {
	mu        sync.Mutex
	calls     []string
	listed    []outlookGraphSubscription
	created   []outlookGraphSubscription
	deleted   []string
	createErr error
	renewErr  error
	next      int
}

func (g *fakeOutlookSubscriptionGraph) request(_ context.Context, method, endpoint, token string, _ map[string]string, body, out any) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if token != "graph-token" {
		return errors.New("unexpected token " + token)
	}
	path := strings.TrimPrefix(endpoint, outlookGraphBaseURL)
	g.calls = append(g.calls, method+" "+path)
	var response any
	switch {
	case method == http.MethodGet && path == "/subscriptions":
		response = outlookGraphSubscriptionsResponse{Value: g.listed}
	case method == http.MethodPost && path == "/subscriptions":
		if g.createErr != nil {
			return g.createErr
		}
		created := body.(outlookGraphSubscription)
		g.next++
		created.ID = "sub-" + string(rune('0'+g.next))
		g.created = append(g.created, created)
		response = created
	case method == http.MethodPatch && strings.HasPrefix(path, "/subscriptions/"):
		if g.renewErr != nil {
			return g.renewErr
		}
		response = body
	case method == http.MethodDelete && strings.HasPrefix(path, "/subscriptions/"):
		g.deleted = append(g.deleted, strings.TrimPrefix(path, "/subscriptions/"))
		return nil
	default:
		return errors.New("unexpected Graph call " + method + " " + path)
	}
	raw, err := json.Marshal(response)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func (g *fakeOutlookSubscriptionGraph) takeCalls() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	calls := g.calls
	g.calls = nil
	return calls
}

func newOutlookSubscriptionTest(t *testing.T) (*SyncOrchestrator, *fakeOutlookSubscriptionGraph) {
	t.Helper()
	db := newLabelSyncTestDB(t)
	for _, account := range []struct{ id, provider string }{{"out-1", providers.ProviderOutlook}, {"gm-1", providers.ProviderGmail}} {
		if _, err := db.Write().Exec(`INSERT INTO accounts (id, user_id, provider, email_address) VALUES (?, 'default', ?, ?)`, account.id, account.provider, account.id+"@example.com"); err != nil {
			t.Fatal(err)
		}
	}
	graph := &fakeOutlookSubscriptionGraph{}
	o := NewSyncOrchestrator(db, nil, nil, labelSyncTestTokens{})
	o.EnableOutlookWebhooks(testOutlookWebhookURL, testOutlookWebhookKey)
	o.outlookGraphRequest = graph.request
	return o, graph
}

func equalCalls(t *testing.T, step string, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("%s: Graph calls\n%s\nwant\n%s", step, strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestOutlookSubscriptionsFollowTheAccounts(t *testing.T) {
	ctx := context.Background()
	o, graph := newOutlookSubscriptionTest(t)
	graph.listed = []outlookGraphSubscription{
		{ID: "old-1", NotificationURL: testOutlookWebhookURL},
		{ID: "other-app", NotificationURL: "https://elsewhere.example/hook"},
	}
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

	o.sweepOutlookSubscriptions(ctx, now)
	equalCalls(t, "first sweep", graph.takeCalls(), "GET /subscriptions", "DELETE /subscriptions/old-1", "POST /subscriptions")
	created := graph.created[0]
	if created.Resource != outlookSubscriptionResource || created.NotificationURL != testOutlookWebhookURL || created.LifecycleURL != testOutlookWebhookURL {
		t.Fatalf("created %#v", created)
	}
	if account, ok := outlookClientStateAccount(testOutlookWebhookKey, created.ClientState); !ok || account != "out-1" {
		t.Fatalf("client state %q names %q %t", created.ClientState, account, ok)
	}
	if !o.outlookSubscribed("out-1", now) || o.outlookSubscribed("gm-1", now) {
		t.Fatal("subscribed accounts are wrong")
	}

	o.sweepOutlookSubscriptions(ctx, now.Add(time.Hour))
	equalCalls(t, "sweep with a fresh subscription", graph.takeCalls())

	o.sweepOutlookSubscriptions(ctx, now.Add(outlookSubscriptionLifetime-outlookSubscriptionRenewal+time.Minute))
	equalCalls(t, "sweep near the end", graph.takeCalls(), "PATCH /subscriptions/sub-1")

	// Graph lost the subscription: a new one is made without listing again.
	graph.renewErr = &providerAPIError{StatusCode: http.StatusNotFound}
	later := now.Add(2*outlookSubscriptionLifetime - 2*outlookSubscriptionRenewal + 2*time.Minute)
	o.sweepOutlookSubscriptions(ctx, later)
	equalCalls(t, "sweep after Graph lost it", graph.takeCalls(), "PATCH /subscriptions/sub-1", "POST /subscriptions")
	graph.renewErr = nil

	if _, err := o.db.Write().Exec(`UPDATE accounts SET email_sync_enabled = 0 WHERE id = 'out-1'`); err != nil {
		t.Fatal(err)
	}
	o.sweepOutlookSubscriptions(ctx, later.Add(time.Minute))
	equalCalls(t, "sweep after receiving stopped", graph.takeCalls(), "DELETE /subscriptions/sub-2")
	if o.outlookSubscribed("out-1", later.Add(time.Minute)) {
		t.Fatal("account still subscribed after receiving stopped")
	}
}

func TestOutlookSubscriptionFailuresWaitBeforeTryingAgain(t *testing.T) {
	ctx := context.Background()
	o, graph := newOutlookSubscriptionTest(t)
	graph.createErr = &providerAPIError{StatusCode: http.StatusBadRequest, Body: "validation failed"}
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

	o.sweepOutlookSubscriptions(ctx, now)
	equalCalls(t, "failed sweep", graph.takeCalls(), "GET /subscriptions", "POST /subscriptions")
	o.sweepOutlookSubscriptions(ctx, now.Add(outlookSubscriptionRetry-time.Second))
	equalCalls(t, "sweep before the retry", graph.takeCalls())
	o.sweepOutlookSubscriptions(ctx, now.Add(outlookSubscriptionRetry))
	equalCalls(t, "second failure", graph.takeCalls(), "POST /subscriptions")
	o.sweepOutlookSubscriptions(ctx, now.Add(2*outlookSubscriptionRetry))
	equalCalls(t, "sweep before the longer retry", graph.takeCalls())

	graph.createErr = nil
	o.sweepOutlookSubscriptions(ctx, now.Add(3*outlookSubscriptionRetry))
	equalCalls(t, "recovery", graph.takeCalls(), "POST /subscriptions")
	if !o.outlookSubscribed("out-1", now.Add(3*outlookSubscriptionRetry)) {
		t.Fatal("account not subscribed after recovering")
	}
}

func TestOutlookNotificationsActOnlyOnSignedAccounts(t *testing.T) {
	ctx := context.Background()
	o, graph := newOutlookSubscriptionTest(t)
	synced := make(chan string, 8)
	o.syncAccountOverride = func(_ context.Context, accountID string, _ bool) error {
		synced <- accountID
		return nil
	}
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	o.sweepOutlookSubscriptions(ctx, now)
	graph.takeCalls()
	state, _ := outlookClientState(testOutlookWebhookKey, "out-1")
	forged := "gm-1" + state[strings.LastIndexByte(state, '.'):]

	o.ReceiveOutlookNotifications([]OutlookNotification{
		{SubscriptionID: "sub-1", ClientState: forged},
		{SubscriptionID: "sub-1", ClientState: state},
		{SubscriptionID: "sub-1", ClientState: state, LifecycleEvent: "missed"},
	})
	select {
	case account := <-synced:
		if account != "out-1" {
			t.Fatalf("synced %q", account)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no sync after a change notification")
	}
	select {
	case account := <-synced:
		t.Fatalf("extra sync of %q", account)
	case <-time.After(100 * time.Millisecond):
	}

	// Graph asks for a renewal long before the end.
	o.ReceiveOutlookNotifications([]OutlookNotification{{SubscriptionID: "sub-1", ClientState: state, LifecycleEvent: "reauthorizationRequired"}})
	o.sweepOutlookSubscriptions(ctx, now.Add(time.Minute))
	equalCalls(t, "sweep after reauthorizationRequired", graph.takeCalls(), "PATCH /subscriptions/sub-1")
	o.sweepOutlookSubscriptions(ctx, now.Add(2*time.Minute))
	equalCalls(t, "sweep after renewing", graph.takeCalls())

	// A notification about an older subscription leaves the current one.
	o.ReceiveOutlookNotifications([]OutlookNotification{{SubscriptionID: "old", ClientState: state, LifecycleEvent: "subscriptionRemoved"}})
	<-synced
	o.sweepOutlookSubscriptions(ctx, now.Add(3*time.Minute))
	equalCalls(t, "sweep after an old subscription ended", graph.takeCalls())

	o.ReceiveOutlookNotifications([]OutlookNotification{{SubscriptionID: "sub-1", ClientState: state, LifecycleEvent: "subscriptionRemoved"}})
	<-synced
	o.sweepOutlookSubscriptions(ctx, now.Add(4*time.Minute))
	equalCalls(t, "sweep after the subscription ended", graph.takeCalls(), "POST /subscriptions")
}
