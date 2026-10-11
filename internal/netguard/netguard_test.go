package netguard

import (
	"context"
	"net"
	"net/netip"
	"strconv"
	"testing"
	"time"
)

func TestIsPrivate(t *testing.T) {
	for _, tc := range []struct {
		addr    string
		private bool
	}{
		{"8.8.8.8", false},
		{"2606:4700:4700::1111", false},
		{"64:ff9b::808:808", false},   // NAT64 for a public address
		{"2002:808:808::1", false},    // 6to4 for a public address
		{"::ffff:8.8.8.8", false},     // mapped public address
		{"127.0.0.1", true},           // loopback
		{"10.1.2.3", true},            // private
		{"172.16.0.1", true},          // private
		{"192.168.1.10", true},        // private
		{"169.254.169.254", true},     // cloud metadata, link-local
		{"100.64.0.1", true},          // carrier-grade NAT
		{"0.0.0.0", true},             // unspecified
		{"0.1.2.3", true},             // "this network"
		{"198.18.0.1", true},          // benchmarking
		{"203.0.113.5", true},         // documentation
		{"255.255.255.255", true},     // broadcast
		{"224.0.0.1", true},           // multicast
		{"::1", true},                 // loopback
		{"fd00::1", true},             // unique local
		{"fe80::1", true},             // link-local
		{"::ffff:127.0.0.1", true},    // mapped loopback
		{"64:ff9b::7f00:1", true},     // NAT64 for loopback
		{"64:ff9b::a00:1", true},      // NAT64 for 10.0.0.1
		{"2002:7f00:1::1", true},      // 6to4 for loopback
		{"2002:c0a8:10a::1", true},    // 6to4 for 192.168.1.10
		{"2001:0:4136:e378::1", true}, // Teredo
	} {
		if got := IsPrivate(netip.MustParseAddr(tc.addr)); got != tc.private {
			t.Errorf("IsPrivate(%s) = %v, want %v", tc.addr, got, tc.private)
		}
	}
	if !IsPrivate(netip.Addr{}) {
		t.Error("an invalid address must count as private")
	}
}

type fakeResolver struct {
	answers map[string][]netip.Addr
	calls   int
}

func (r *fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	r.calls++
	return r.answers[host], nil
}

func listen(t *testing.T) (port int, accepted chan struct{}) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	accepted = make(chan struct{}, 4)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			accepted <- struct{}{}
			_ = conn.Close()
		}
	}()
	return listener.Addr().(*net.TCPAddr).Port, accepted
}

func assertNoConnection(t *testing.T, accepted chan struct{}) {
	t.Helper()
	select {
	case <-accepted:
		t.Fatal("a refused target was still contacted")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestDialRefusesPrivateTargets(t *testing.T) {
	port, accepted := listen(t)
	resolver := &fakeResolver{answers: map[string][]netip.Addr{
		"mail.internal.test": {netip.MustParseAddr("127.0.0.1")},
		"mixed.example.test": {netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("127.0.0.1")},
	}}
	dialer := &Dialer{Dialer: &net.Dialer{Timeout: time.Second}, Resolver: resolver}
	for _, host := range []string{"mail.internal.test", "mixed.example.test", "127.0.0.1", "[::1]", "::ffff:127.0.0.1"} {
		_, err := dialer.Dial(t.Context(), "imap", host, port, false)
		if !IsBlocked(err) {
			t.Fatalf("Dial(%s) error = %v, want a private target refusal", host, err)
		}
	}
	assertNoConnection(t, accepted)
	if _, err := dialer.Dial(t.Context(), "imap", "fe80::1%eth0", port, false); err == nil || IsBlocked(err) {
		t.Fatalf("Dial with a zone error = %v, want a plain refusal", err)
	}
}

// An approved name connects to the address that was resolved once and checked,
// so a second DNS answer can't redirect the connection.
func TestDialConnectsToTheCheckedAddress(t *testing.T) {
	port, accepted := listen(t)
	resolver := &fakeResolver{answers: map[string][]netip.Addr{"mail.internal.test": {netip.MustParseAddr("127.0.0.1")}}}
	dialer := &Dialer{Dialer: &net.Dialer{Timeout: time.Second}, Resolver: resolver}
	conn, err := dialer.Dial(t.Context(), "imap", "mail.internal.test", port, true)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if got := conn.RemoteAddr().String(); got != net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) {
		t.Fatalf("connected to %s", got)
	}
	if resolver.calls != 1 {
		t.Fatalf("resolved %d times, want once", resolver.calls)
	}
	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("approved target was not contacted")
	}
}

func TestBlockedErrorNamesTheSetting(t *testing.T) {
	err := &BlockedError{Protocol: "imap", Host: "192.168.1.10", Port: 993}
	want := "IMAP server 192.168.1.10:993 is on a private network; it needs a private target exception under Mail security"
	if err.Error() != want {
		t.Fatalf("Error() = %q", err.Error())
	}
}
