package mail

import (
	"fmt"
	"slices"
	"testing"
)

func takeTypes(sub *Subscription) []string {
	var got []string
	for _, event := range sub.Take() {
		got = append(got, fmt.Sprintf("%s:%s:%d", event.Type, event.FolderID, event.Current))
	}
	return got
}

func TestSubscriptionsReceiveOnlyTheirEvents(t *testing.T) {
	bus := NewEventBus()
	alice := bus.SubscribeFor(SubscribeOptions{UserID: "alice", AccountIDs: []string{"alice-mail"}})
	defer alice.Close()
	bob := bus.SubscribeFor(SubscribeOptions{UserID: "bob", AnyAccount: true})
	defer bob.Close()
	admin := bus.SubscribeFor(SubscribeOptions{UserID: "carol", Admin: true})
	defer admin.Close()
	worker := bus.SubscribeFor(SubscribeOptions{All: true, Types: []EventType{EventNewMail}})
	defer worker.Close()

	bus.Publish(Event{Type: EventMutation, UserID: "alice", FolderID: "a"})
	bus.Publish(Event{Type: EventAvatarUpdated, UserIDs: []string{"bob", "carol", "bob"}})
	bus.Publish(Event{Type: EventContactBackfill, Payload: map[string]any{"user_id": "carol"}})
	bus.Publish(Event{Type: EventNewMail, AccountID: "alice-mail", FolderID: "inbox"})
	bus.Publish(Event{Type: EventAvatarBackfill, AdminOnly: true})
	bus.Publish(Event{Type: EventSyncComplete}) // names nobody

	for _, tt := range []struct {
		name string
		sub  *Subscription
		want []string
	}{
		{"alice", alice, []string{"mutation:a:0", "new-mail:inbox:0"}},
		{"bob", bob, []string{"avatar-updated::0", "new-mail:inbox:0"}},
		{"admin", admin, []string{"avatar-updated::0", "contact-backfill::0", "avatar-backfill::0"}},
		{"worker", worker, []string{"new-mail:inbox:0"}},
	} {
		if got := takeTypes(tt.sub); !slices.Equal(got, tt.want) {
			t.Errorf("%s received %v, want %v", tt.name, got, tt.want)
		}
	}
	select {
	case <-alice.Ready():
		t.Fatal("ready again with nothing waiting")
	default:
	}
}

// Waiting progress for a folder is replaced by newer progress, but never
// moves past an event published after it for that folder.
func TestSubscriptionKeepsLatestWaitingProgress(t *testing.T) {
	bus := NewEventBus()
	sub := bus.SubscribeFor(SubscribeOptions{UserID: "alice"})
	defer sub.Close()
	publish := func(kind EventType, folder string, current int) {
		bus.Publish(Event{Type: kind, UserID: "alice", AccountID: "acc", FolderID: folder, Current: current})
	}
	publish(EventSyncStarted, "inbox", 0)
	publish(EventSyncProgress, "inbox", 10)
	publish(EventSyncProgress, "sent", 5)
	publish(EventSyncProgress, "inbox", 20)
	publish(EventNewMail, "archive", 0)
	publish(EventSyncProgress, "inbox", 30)
	publish(EventSyncComplete, "inbox", 0)
	publish(EventSyncProgress, "inbox", 1)
	publish(EventSyncProgress, "inbox", 2)
	want := []string{"sync-started:inbox:0", "sync-progress:inbox:30", "sync-progress:sent:5", "new-mail:archive:0", "sync-complete:inbox:0", "sync-progress:inbox:2"}
	if got := takeTypes(sub); !slices.Equal(got, want) {
		t.Fatalf("received %v, want %v", got, want)
	}
	// Once taken, progress starts a new entry.
	publish(EventSyncProgress, "inbox", 3)
	if got := takeTypes(sub); !slices.Equal(got, []string{"sync-progress:inbox:3"}) {
		t.Fatalf("after take %v", got)
	}
}

func TestSubscriptionLimitsWaitingEventsAndStopsOnClose(t *testing.T) {
	bus := NewEventBus()
	sub := bus.SubscribeFor(SubscribeOptions{UserID: "alice"})
	for i := range subscriptionQueueLimit + 10 {
		bus.Publish(Event{Type: EventMutation, UserID: "alice", Current: i})
	}
	if got := len(sub.Take()); got != subscriptionQueueLimit {
		t.Fatalf("%d events waiting, want %d", got, subscriptionQueueLimit)
	}
	sub.Close()
	bus.Publish(Event{Type: EventMutation, UserID: "alice"})
	if got := sub.Take(); len(got) != 0 {
		t.Fatalf("closed subscription received %v", got)
	}
	if len(bus.users) != 0 {
		t.Fatalf("closed subscription still indexed: %v", bus.users)
	}
}

// BenchmarkEventFanOut publishes one user's sync progress with 1,000 people
// connected. "every connection" is how events were delivered before
// subscriptions were routed by user.
func BenchmarkEventFanOut(b *testing.B) {
	for _, mode := range []string{"routed", "every connection"} {
		b.Run(mode, func(b *testing.B) {
			bus := NewEventBus()
			for i := range 1000 {
				opts := SubscribeOptions{UserID: fmt.Sprintf("user-%d", i), AnyAccount: true}
				if mode != "routed" {
					opts = SubscribeOptions{All: true}
				}
				sub := bus.SubscribeFor(opts)
				b.Cleanup(sub.Close)
				go func() {
					for range sub.Ready() {
						sub.Take()
					}
				}()
			}
			event := Event{Type: EventSyncProgress, UserID: "user-7", AccountID: "acc", FolderID: "inbox"}
			b.ResetTimer()
			for range b.N {
				bus.Publish(event)
			}
		})
	}
}
