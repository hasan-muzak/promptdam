// Command detector runs the detector service: the firewall's model, read
// over HTTP by the gateways on their private network.
//
//	DETECTOR_ADDR        where it listens (default :8484)
//	DETECTOR_MODEL_DIR   the model directory (engine.Load)
//	DETECTOR_TOKEN       the bearer token gateways send (required unless
//	                     DETECTOR_DEV=1)
//	DETECTOR_ORT_LIB     ONNX Runtime's shared library
//	DETECTOR_DEVICE      cpu (default) or cuda
//	DETECTOR_THREADS     ONNX Runtime's intra-op threads
//	DETECTOR_WORKERS, DETECTOR_MAX_BATCH, DETECTOR_MAX_WAIT_MS,
//	DETECTOR_MAX_QUEUED  the batching (engine.EngineOptions)
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	ort "github.com/yalue/onnxruntime_go"

	"github.com/hasan-muzak/promptdam/detector"
	"github.com/hasan-muzak/promptdam/detector/engine"
)

func main() {
	if err := run(); err != nil {
		slog.Error("detector", "err", err)
		os.Exit(1)
	}
}

func run() error {
	token := os.Getenv("DETECTOR_TOKEN")
	if token == "" && os.Getenv("DETECTOR_DEV") != "1" {
		return errors.New("DETECTOR_TOKEN is required: the service receives raw personal data")
	}
	if lib := os.Getenv("DETECTOR_ORT_LIB"); lib != "" {
		ort.SetSharedLibraryPath(lib)
	}
	if err := ort.InitializeEnvironment(); err != nil {
		return err
	}
	defer func() { _ = ort.DestroyEnvironment() }()
	m, err := engine.Load(os.Getenv("DETECTOR_MODEL_DIR"), engine.Options{Threads: num("DETECTOR_THREADS", 0),
		Device: os.Getenv("DETECTOR_DEVICE")})
	if err != nil {
		return err
	}
	defer m.Close()
	e := engine.NewEngine(m, engine.EngineOptions{Workers: num("DETECTOR_WORKERS", 0), MaxBatch: num("DETECTOR_MAX_BATCH", 0),
		MaxWait: time.Duration(num("DETECTOR_MAX_WAIT_MS", 0)) * time.Millisecond, MaxQueued: int64(num("DETECTOR_MAX_QUEUED", 0))})
	e.Start()
	defer e.Stop()
	s := &detector.Server{Engine: e, Version: m.Version, Token: token}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := s.WarmUp(ctx); err != nil {
		return err
	}
	addr := os.Getenv("DETECTOR_ADDR")
	if addr == "" {
		addr = ":8484"
	}
	srv := &http.Server{Addr: addr, Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 120 * time.Second}
	slog.Info("detector: serving", "addr", addr, "model", m.Version)
	errs := make(chan error, 1)
	go func() { errs <- srv.ListenAndServe() }()
	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return srv.Shutdown(sctx)
}

func num(k string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil {
		return v
	}
	return def
}
