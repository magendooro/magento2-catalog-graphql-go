package main

import (
	"log"

	"github.com/magendooro/magento2-catalog-graphql-go/internal/app"
	"github.com/magendooro/magento2-catalog-graphql-go/internal/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	a, err := app.New(cfg)
	if err != nil {
		log.Fatalf("failed to initialize app: %v", err)
	}

	if err := a.Run(); err != nil {
		log.Fatalf("app error: %v", err)
	}
}
