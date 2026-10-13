package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/cristianadrielbraun/gofer/internal/storage"
)

// diagnosticsConfig holds the optional performance diagnostics. Both are off
// unless configured.
type diagnosticsConfig struct {
	slowQuery    time.Duration
	profilerAddr string
}

func loadDiagnosticsConfig() (diagnosticsConfig, error) {
	var config diagnosticsConfig
	if value := strings.TrimSpace(os.Getenv("GOFER_SLOW_QUERY_MS")); value != "" {
		ms, err := strconv.Atoi(value)
		if err != nil || ms < 1 {
			return config, errors.New("GOFER_SLOW_QUERY_MS must be a positive number of milliseconds")
		}
		config.slowQuery = time.Duration(ms) * time.Millisecond
	}
	if value := strings.TrimSpace(os.Getenv("GOFER_PPROF_ADDR")); value != "" {
		if err := requireLoopbackAddr(value); err != nil {
			return config, fmt.Errorf("GOFER_PPROF_ADDR: %w", err)
		}
		config.profilerAddr = value
	}
	return config, nil
}

// The profiler exposes memory contents and has no authentication, so it only
// listens on loopback. Reach it remotely through an SSH tunnel.
func requireLoopbackAddr(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("the profiler only listens on a loopback address such as 127.0.0.1:6060")
	}
	return nil
}

// startDiagnostics applies the slow query threshold and starts the profiler.
// It must run before any database is opened. The returned function stops the
// profiler.
func startDiagnostics(config diagnosticsConfig) (func(), error) {
	storage.SetSlowQueryThreshold(config.slowQuery)
	if config.slowQuery > 0 {
		log.Printf("diagnostics: logging database work slower than %s", config.slowQuery)
	}
	if config.profilerAddr == "" {
		return func() {}, nil
	}
	addr, stop, err := startProfiler(config.profilerAddr)
	if err != nil {
		return nil, err
	}
	log.Printf("diagnostics: profiler listening on http://%s/debug/pprof/", addr)
	return stop, nil
}

func startProfiler(addr string) (net.Addr, func(), error) {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("start profiler: %w", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("diagnostics: profiler stopped: %v", err)
		}
	}()
	return listener.Addr(), func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}, nil
}
