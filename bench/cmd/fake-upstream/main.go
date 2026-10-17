// Command fake-upstream serves bench/fakeupstream, a stand-in LLM provider
// speaking the OpenAI and Anthropic APIs, for local development and load
// tests: `task promptdam:dev:fake`, then `api dev seed` points a project's
// providers at it.
package main

import (
	"flag"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/hasan-muzak/promptdam/bench/fakeupstream"
)

func main() {
	addr := flag.String("addr", ":8299", "listen address")
	ttfb := flag.Duration("ttfb", 200*time.Millisecond, "delay before the response headers")
	delay := flag.Duration("chunk-delay", 20*time.Millisecond, "pause before each streamed chunk")
	chunks := flag.Int("chunks", 20, "content chunks (and output tokens) per answer")
	flag.Parse()
	srv := &http.Server{
		Addr:              *addr,
		Handler:           fakeupstream.New(fakeupstream.Options{TTFB: *ttfb, ChunkDelay: *delay, Chunks: *chunks, Forget: true}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	slog.Info("fake provider listening", "addr", *addr, "base_url", "http://localhost"+*addr+"/v1")
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("fake provider", "err", err)
		os.Exit(1)
	}
}
