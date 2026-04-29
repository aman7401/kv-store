package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aman7401/kv-store/internal/api"
	"github.com/aman7401/kv-store/internal/lsm"
	"github.com/aman7401/kv-store/internal/store"
	"github.com/aman7401/kv-store/internal/ttl"
	"github.com/aman7401/kv-store/internal/wal"
)

const (
	walPath     = "wal/kv.wal"
	ttlInterval = 5 * time.Second
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	engine := flag.String("engine", "hashmap", "storage engine: hashmap | lsm")
	flag.Parse()

	mux := http.NewServeMux()

	switch *engine {
	case "lsm":
		startLSM(mux)
	default:
		startHashMap(mux)
	}

	srv := &http.Server{
		Addr:         *addr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Printf("kv-store listening on %s (engine=%s)", *addr, *engine)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-quit
	log.Println("shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("server forced shutdown: %v", err)
	}
	log.Println("server stopped cleanly")
}

func startHashMap(mux *http.ServeMux) {
	if err := os.MkdirAll("wal", 0755); err != nil {
		log.Fatalf("create wal dir: %v", err)
	}

	kv := store.New()

	log.Println("replaying WAL...")
	if err := wal.Replay(walPath, func(r wal.Record) error {
		switch r.Op {
		case wal.OpSet:
			if !r.ExpiresAt.IsZero() && time.Now().After(r.ExpiresAt) {
				return nil
			}
			kv.SetWithExpiry(r.Key, r.Value, r.ExpiresAt)
		case wal.OpDelete:
			kv.Delete(r.Key)
		}
		return nil
	}); err != nil {
		log.Fatalf("wal replay: %v", err)
	}
	log.Printf("recovered %d keys from WAL", kv.Len())

	w, err := wal.Open(walPath)
	if err != nil {
		log.Fatalf("open wal: %v", err)
	}

	cleaner := ttl.NewCleaner(
		ttlInterval,
		kv.Keys,
		func(key string) bool { _, err := kv.Get(key); return err != nil },
		kv.Delete,
	)
	cleaner.Start()

	api.New(kv, w).RegisterRoutes(mux)
}

func startLSM(mux *http.ServeMux) {
	tree, err := lsm.NewTree("data/lsm", 4*1024*1024, 8) // flush at 4MB, compact at 8 tables
	if err != nil {
		log.Fatalf("lsm init: %v", err)
	}
	api.NewLSM(tree).RegisterRoutes(mux)
}
