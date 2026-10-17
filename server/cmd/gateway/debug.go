package main

import (
	"expvar"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"runtime"
	"time"

	"github.com/hasan-muzak/promptdam/server/features/requestlog"
)

// debugServer serves pprof and expvar (memory, goroutines, the request
// log's dropped rows) on a loopback address only: a load test on the same
// machine reads them, and an operator through an SSH tunnel, nobody else.
func debugServer(addr string, log *requestlog.Log) (*http.Server, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("PROMPTDAM_GATEWAY_DEBUG_ADDR %q: %w", addr, err)
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, fmt.Errorf("PROMPTDAM_GATEWAY_DEBUG_ADDR %q: only a loopback address (127.0.0.1:port)", addr)
	}
	expvar.Publish("goroutines", expvar.Func(func() any { return runtime.NumGoroutine() }))
	expvar.Publish("requestlog_dropped", expvar.Func(func() any { return log.Dropped() }))
	mux := http.NewServeMux()
	mux.Handle("GET /debug/vars", expvar.Handler())
	mux.HandleFunc("GET /debug/pprof/", pprof.Index)
	mux.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)
	return &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}, nil
}
