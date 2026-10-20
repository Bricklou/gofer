package jmap

import "testing"

func TestParseEndpoint(t *testing.T) {
	cases := map[string]Endpoint{
		"https://Mail.Example.Test/.well-known/jmap":   {"https", "mail.example.test", 443},
		"http://192.168.1.20:8080/.well-known/jmap":    {"http", "192.168.1.20", 8080},
		"HTTP://mail.example.test./jmap/session":       {"http", "mail.example.test", 80},
		"https://[2001:DB8::1]:8443/.well-known/jmap":  {"https", "2001:db8::1", 8443},
		" https://mail.example.test:443/jmap/session ": {"https", "mail.example.test", 443},
	}
	for raw, want := range cases {
		got, err := ParseEndpoint(raw)
		if err != nil || got != want {
			t.Errorf("ParseEndpoint(%q) = %+v, %v; want %+v", raw, got, err, want)
		}
	}
	for _, raw := range []string{"ftp://mail.example.test/", "https:///jmap", "https://mail.example.test:0/", "https://mail.example.test:99999/", "mail.example.test"} {
		if got, err := ParseEndpoint(raw); err == nil {
			t.Errorf("ParseEndpoint(%q) = %+v, want error", raw, got)
		}
	}
	if got := (Endpoint{"https", "2001:db8::1", 8443}).String(); got != "https://[2001:db8::1]:8443" {
		t.Errorf("String() = %q", got)
	}
}
