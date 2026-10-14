// Command v2v-bot is the Telegram frontend of the v2v engine: it long-polls
// Telegram, forwards each update to the engine's channel API and delivers the
// answer. It holds no conversation logic.
package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"

	"github.com/valpere/v2v-bot/internal/adapter"
	"github.com/valpere/v2v-bot/internal/config"
	"github.com/valpere/v2v-bot/internal/engineclient"
	"github.com/valpere/v2v-bot/internal/telegram"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	tg, err := telegram.New(cfg.Token)
	if err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	updates, err := tg.Updates(ctx)
	if err != nil {
		log.Fatal(err)
	}
	a := adapter.New(tg, engineclient.New(cfg.EngineURL, cfg.EngineKey, cfg.EngineTimeout))
	log.Printf("v2v-bot: listening, engine %s", cfg.EngineURL)
	a.Run(ctx, updates)
	log.Print("v2v-bot: shut down")
}
