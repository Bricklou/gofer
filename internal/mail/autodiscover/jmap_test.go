package autodiscover

import (
	"context"
	"net"
	"reflect"
	"testing"
)

func TestJMAPSessionCandidatesFromEmail(t *testing.T) {
	resolver := fakeResolver{
		"jmap.tcp.example.test": {
			{Target: "backup.example.test.", Port: 443, Priority: 20, Weight: 0},
			{Target: "jmap.example.test.", Port: 8443, Priority: 10, Weight: 5},
			{Target: "Light.Example.Test.", Port: 443, Priority: 10, Weight: 50},
		},
	}
	got, err := JMAPSessionCandidates(context.Background(), "robin@Example.Test", "", resolver)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"https://light.example.test:443/.well-known/jmap",
		"https://jmap.example.test:8443/.well-known/jmap",
		"https://backup.example.test:443/.well-known/jmap",
		"https://example.test/.well-known/jmap",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %v\nwant %v", got, want)
	}
}

func TestJMAPSessionCandidatesWithoutSRV(t *testing.T) {
	// A lone "." target says the domain has no JMAP service.
	resolver := fakeResolver{"jmap.tcp.example.test": {{Target: ".", Port: 0}}}
	got, err := JMAPSessionCandidates(context.Background(), "robin@example.test", "", resolver)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"https://example.test/.well-known/jmap"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
	if _, err := JMAPSessionCandidates(context.Background(), "not-an-address", "", resolver); err == nil {
		t.Fatal("accepted an invalid email")
	}
}

func TestJMAPSessionCandidatesFromServer(t *testing.T) {
	cases := map[string][]string{
		"https://api.example.test/jmap/session": {"https://api.example.test/jmap/session"},
		"http://192.168.1.20:8080/jmap/session": {"http://192.168.1.20:8080/jmap/session"},
		"api.example.test/jmap/session":         {"https://api.example.test/jmap/session"},
		"Mail.Example.Test":                     {"https://mail.example.test/.well-known/jmap", "http://mail.example.test/.well-known/jmap"},
		"192.168.1.20:8080":                     {"https://192.168.1.20:8080/.well-known/jmap", "http://192.168.1.20:8080/.well-known/jmap"},
		"[2001:db8::1]:8443":                    {"https://[2001:db8::1]:8443/.well-known/jmap", "http://[2001:db8::1]:8443/.well-known/jmap"},
		"2001:db8::1":                           {"https://[2001:db8::1]/.well-known/jmap", "http://[2001:db8::1]/.well-known/jmap"},
	}
	for server, want := range cases {
		got, err := JMAPSessionCandidates(context.Background(), "robin@example.test", server, fakeResolver{})
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("server %q: candidates = %v, %v\nwant %v", server, got, err, want)
		}
	}
	for _, server := range []string{"ftp://mail.example.test/", "https:///jmap", "mail.example.test:0", "mail.example.test:99999", "robin@mail.example.test"} {
		if got, err := JMAPSessionCandidates(context.Background(), "robin@example.test", server, fakeResolver{}); err == nil {
			t.Errorf("server %q: candidates = %v, want error", server, got)
		}
	}
}

// The server field wins over DNS, so no lookup happens at all.
func TestJMAPSessionCandidatesServerSkipsDNS(t *testing.T) {
	var looked bool
	resolver := srvFunc(func() { looked = true })
	if _, err := JMAPSessionCandidates(context.Background(), "robin@example.test", "mail.example.test", resolver); err != nil {
		t.Fatal(err)
	}
	if looked {
		t.Fatal("looked up DNS although a server was given")
	}
}

type srvFunc func()

func (f srvFunc) LookupSRV(context.Context, string, string, string) (string, []*net.SRV, error) {
	f()
	return "", nil, nil
}
