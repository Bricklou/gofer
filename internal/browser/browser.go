// Package browser opens a URL in the desktop's default browser.
package browser

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// Open starts the default browser on url and returns once the launcher has
// started. done, if not nil, receives the launcher's result when it exits.
// The launcher gets no terminal, so a text-mode browser cannot take over
// Gofer's console.
func Open(url string, done func(error)) error {
	command, err := launcher(runtime.GOOS, isWSL(), exec.LookPath)
	if err != nil {
		return err
	}
	cmd := exec.Command(command[0], append(command[1:], url)...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		err := cmd.Wait()
		if done != nil {
			done(err)
		}
	}()
	return nil
}

// launcher returns the command, without the URL, that opens a URL on goos.
func launcher(goos string, wsl bool, lookPath func(string) (string, error)) ([]string, error) {
	switch goos {
	case "darwin":
		return []string{"open"}, nil
	case "windows":
		return []string{"rundll32", "url.dll,FileProtocolHandler"}, nil
	}
	candidates := [][]string{{"xdg-open"}, {"x-www-browser"}}
	if wsl {
		// A Linux browser opener inside WSL usually has no desktop to open
		// in, so prefer the Windows one: wslview when wslu is installed, or
		// Windows' own handler through WSL's interop.
		candidates = append([][]string{{"wslview"}, {"rundll32.exe", "url.dll,FileProtocolHandler"}}, candidates...)
	}
	names := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if _, err := lookPath(candidate[0]); err == nil {
			return candidate, nil
		}
		names = append(names, candidate[0])
	}
	return nil, fmt.Errorf("no browser opener found (looked for %s)", strings.Join(names, ", "))
}

// isWSL reports whether this is Linux running under Windows Subsystem for
// Linux. WSL sets WSL_DISTRO_NAME for login shells, and both WSL 1 and 2
// name Microsoft in the kernel release.
func isWSL() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	if os.Getenv("WSL_DISTRO_NAME") != "" {
		return true
	}
	release, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(release)), "microsoft")
}
