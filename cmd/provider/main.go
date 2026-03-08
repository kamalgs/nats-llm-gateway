package main

import (
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/kamalgs/infermesh/internal/config"
	"github.com/kamalgs/infermesh/internal/provider/anthropic"
	"github.com/kamalgs/infermesh/internal/provider/ollama"
	"github.com/kamalgs/infermesh/internal/provider/openai"
	"github.com/nats-io/nats.go"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	providerName := os.Getenv("PROVIDER_NAME")
	if providerName == "" {
		log.Error("PROVIDER_NAME env var is required")
		os.Exit(1)
	}

	cfgPath := os.Getenv("PROVIDER_CONFIG")
	if cfgPath == "" {
		cfgPath = "configs/provider.yaml"
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Error("failed to load config", "path", cfgPath, "error", err)
		os.Exit(1)
	}

	provCfg, ok := cfg.Providers[providerName]
	if !ok {
		log.Error("provider not found in config", "provider", providerName)
		os.Exit(1)
	}

	natsURL := os.Getenv("NATS_URL")
	if natsURL == "" {
		natsURL = cfg.NATS.URL
	}

	nc, err := nats.Connect(natsURL)
	if err != nil {
		log.Error("failed to connect to nats", "url", natsURL, "error", err)
		os.Exit(1)
	}
	defer nc.Close()
	log.Info("connected to nats", "url", natsURL)

	plog := log.With("component", fmt.Sprintf("provider-%s", providerName))

	var sub *nats.Subscription
	switch providerName {
	case "openai":
		adapter := openai.NewAdapter(provCfg, plog)
		sub, err = adapter.Subscribe(nc)
	case "anthropic":
		adapter := anthropic.NewAdapter(provCfg, plog)
		sub, err = adapter.Subscribe(nc)
	case "ollama":
		adapter := ollama.NewAdapter(provCfg, plog)
		sub, err = adapter.Subscribe(nc)
	default:
		log.Error("unknown provider", "provider", providerName)
		os.Exit(1)
	}

	if err != nil {
		log.Error("failed to start adapter", "provider", providerName, "error", err)
		os.Exit(1)
	}
	defer sub.Drain()

	log.Info("provider adapter ready", "provider", providerName)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Info("shutting down", "provider", providerName)
}
