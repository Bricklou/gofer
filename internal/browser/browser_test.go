package browser

import (
	"errors"
	"slices"
	"testing"
)

func lookPathFor(installed ...string) func(string) (string, error) {
	return func(name string) (string, error) {
		if slices.Contains(installed, name) {
			return "/usr/bin/" + name, nil
		}
		return "", errors.New("not found")
	}
}

func TestLauncher(t *testing.T) {
	tests := []struct {
		name      string
		goos      string
		wsl       bool
		installed []string
		want      []string
	}{
		{"macOS", "darwin", false, nil, []string{"open"}},
		{"Windows", "windows", false, nil, []string{"rundll32", "url.dll,FileProtocolHandler"}},
		{"Linux desktop", "linux", false, []string{"xdg-open", "x-www-browser"}, []string{"xdg-open"}},
		{"Linux without xdg-utils", "linux", false, []string{"x-www-browser"}, []string{"x-www-browser"}},
		{"FreeBSD", "freebsd", false, []string{"xdg-open"}, []string{"xdg-open"}},
		{"WSL with wslu", "linux", true, []string{"wslview", "rundll32.exe", "xdg-open"}, []string{"wslview"}},
		{"WSL without wslu", "linux", true, []string{"rundll32.exe", "xdg-open"}, []string{"rundll32.exe", "url.dll,FileProtocolHandler"}},
		{"WSL without Windows interop", "linux", true, []string{"xdg-open"}, []string{"xdg-open"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := launcher(test.goos, test.wsl, lookPathFor(test.installed...))
			if err != nil {
				t.Fatalf("launcher() error = %v", err)
			}
			if !slices.Equal(got, test.want) {
				t.Fatalf("launcher() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestLauncherWithoutOpener(t *testing.T) {
	_, err := launcher("linux", true, lookPathFor())
	if err == nil {
		t.Fatal("launcher() succeeded with no opener installed")
	}
	want := "no browser opener found (looked for wslview, rundll32.exe, xdg-open, x-www-browser)"
	if err.Error() != want {
		t.Fatalf("launcher() error = %q, want %q", err, want)
	}
}
