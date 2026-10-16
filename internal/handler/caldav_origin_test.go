package handler

import "testing"

func TestResolveCalDAVHrefFollowsICloudPartitionServer(t *testing.T) {
	got, err := resolveCalDAVHref("https://caldav.icloud.com/", "https://p63-caldav.icloud.com:443/123456789/calendars/")
	if err != nil {
		t.Fatalf("resolveCalDAVHref() error = %v", err)
	}
	if got != "https://p63-caldav.icloud.com:443/123456789/calendars/" {
		t.Fatalf("resolveCalDAVHref() = %q", got)
	}
	if _, err := resolveCalDAVHref("https://caldav.icloud.com", got+"home/"); err != nil {
		t.Fatalf("saved iCloud calendar rejected against its base URL: %v", err)
	}
}

func TestResolveCalDAVHrefAcceptsSameRegisteredDomain(t *testing.T) {
	for _, tc := range []struct{ base, href string }{
		{"https://caldav.icloud.com/", "https://calendars-eu.icloud.com/123/"},
		{"https://dav.example.org/", "https://dav.example.org:443/calendars/robin/"},
		{"https://dav.example.co.uk/", "https://users.example.co.uk/robin/"},
	} {
		if _, err := resolveCalDAVHref(tc.base, tc.href); err != nil {
			t.Errorf("resolveCalDAVHref(%q, %q) error = %v", tc.base, tc.href, err)
		}
	}
}

func TestResolveCalDAVHrefRejectsOtherServers(t *testing.T) {
	for _, tc := range []struct{ base, href string }{
		{"https://caldav.icloud.com/", "https://evil.example/calendars/"},
		{"https://caldav.icloud.com/", "https://caldav.icloud.com.evil.example/123/"},
		{"https://caldav.icloud.com/", "https://p63-caldav.icloud.com:8443/123/"},
		{"https://dav.example.org/", "https://dav.example.net/calendars/"},
		{"https://dav.example.co.uk/", "https://dav.other.co.uk/calendars/"},
		{"https://robin.github.io/", "https://mallory.github.io/calendars/"},
		{"http://dav.example.org/", "http://users.example.org/calendars/"},
		{"https://dav.example.org/", "http://users.example.org/calendars/"},
		{"https://192.0.2.10/", "https://192.0.2.11/calendars/"},
		{"https://localhost/", "https://otherhost/calendars/"},
	} {
		if _, err := resolveCalDAVHref(tc.base, tc.href); err == nil {
			t.Errorf("resolveCalDAVHref(%q, %q) accepted a different server", tc.base, tc.href)
		}
	}
}
