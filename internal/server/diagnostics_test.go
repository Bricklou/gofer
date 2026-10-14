package server

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestDiagnosticsConfig(t *testing.T) {
	t.Setenv("GOFER_SLOW_QUERY_MS", "")
	t.Setenv("GOFER_PPROF_ADDR", "")
	config, err := loadDiagnosticsConfig()
	if err != nil || config.slowQuery != 0 || config.profilerAddr != "" {
		t.Fatalf("unset config = %+v, %v; want everything off", config, err)
	}

	t.Setenv("GOFER_SLOW_QUERY_MS", "250")
	t.Setenv("GOFER_PPROF_ADDR", "127.0.0.1:0")
	config, err = loadDiagnosticsConfig()
	if err != nil || config.slowQuery != 250*time.Millisecond || config.profilerAddr != "127.0.0.1:0" {
		t.Fatalf("config = %+v, %v", config, err)
	}

	for _, value := range []string{"0", "-5", "fast"} {
		t.Setenv("GOFER_SLOW_QUERY_MS", value)
		if _, err := loadDiagnosticsConfig(); err == nil {
			t.Errorf("GOFER_SLOW_QUERY_MS=%q accepted", value)
		}
	}
	t.Setenv("GOFER_SLOW_QUERY_MS", "")

	for _, addr := range []string{"localhost:6060", "[::1]:6060", "127.0.0.2:6060"} {
		t.Setenv("GOFER_PPROF_ADDR", addr)
		if _, err := loadDiagnosticsConfig(); err != nil {
			t.Errorf("GOFER_PPROF_ADDR=%q rejected: %v", addr, err)
		}
	}
	for _, addr := range []string{":6060", "0.0.0.0:6060", "192.168.1.10:6060", "example.test:6060", "6060"} {
		t.Setenv("GOFER_PPROF_ADDR", addr)
		if _, err := loadDiagnosticsConfig(); err == nil {
			t.Errorf("GOFER_PPROF_ADDR=%q accepted", addr)
		}
	}
}

func TestProfilerServesOnLoopback(t *testing.T) {
	addr, stop, err := startProfiler("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	response, err := http.Get("http://" + addr.String() + "/debug/pprof/")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "goroutine") {
		t.Fatalf("profiler index status %d: %s", response.StatusCode, body)
	}
}
