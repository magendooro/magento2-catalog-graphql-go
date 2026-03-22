package app

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/extension"
	"github.com/99designs/gqlgen/graphql/playground"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/magendooro/magento2-catalog-graphql-go/internal/repository"

	"github.com/magendooro/magento2-catalog-graphql-go/graph"
	"github.com/magendooro/magento2-catalog-graphql-go/internal/cache"
	"github.com/magendooro/magento2-catalog-graphql-go/internal/config"
	"github.com/magendooro/magento2-catalog-graphql-go/internal/database"
	"github.com/magendooro/magento2-catalog-graphql-go/internal/middleware"
)

type App struct {
	cfg   *config.Config
	db    *sql.DB
	cache *cache.Client
}

func New(cfg *config.Config) (*App, error) {
	// Configure logging
	if cfg.Logging.Pretty {
		log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})
	}
	level, err := zerolog.ParseLevel(cfg.Logging.Level)
	if err != nil {
		level = zerolog.InfoLevel
	}
	zerolog.SetGlobalLevel(level)

	// Database connection
	db, err := database.NewConnection(cfg.Database)
	if err != nil {
		return nil, fmt.Errorf("database connection failed: %w", err)
	}
	log.Info().Str("database", cfg.Database.Name).Msg("connected to database")

	// Redis cache (optional — nil if unavailable)
	redisCache := cache.New(cache.Config{
		Host:     cfg.Redis.Host,
		Port:     cfg.Redis.Port,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	})

	return &App{cfg: cfg, db: db, cache: redisCache}, nil
}

func (a *App) Run() error {
	// Set media cache hash if configured
	if hash := a.cfg.Media.CacheHash; hash != "" {
		repository.ImageCacheHash = hash
		log.Info().Str("hash", hash).Msg("media cache hash configured")
	}

	// Store resolver middleware
	storeResolver := middleware.NewStoreResolver(a.db)

	// GraphQL server
	resolver, err := graph.NewResolver(a.db, a.cfg)
	if err != nil {
		return fmt.Errorf("failed to create resolver: %w", err)
	}
	srv := handler.NewDefaultServer(graph.NewExecutableSchema(graph.Config{
		Resolvers: resolver,
	}))

	// GraphQL query complexity and depth limits
	if a.cfg.GraphQL.ComplexityLimit > 0 {
		srv.Use(extension.FixedComplexityLimit(a.cfg.GraphQL.ComplexityLimit))
	}

	// HTTP mux
	mux := http.NewServeMux()
	mux.Handle("/graphql", srv)
	mux.Handle("/{$}", playground.Handler("Magento Catalog GraphQL", "/graphql"))
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if err := a.db.Ping(); err != nil {
			http.Error(w, "database unhealthy", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	// Apply middleware chain (outermost first)
	var handler http.Handler = mux
	handler = middleware.CacheMiddleware(a.cache)(handler)
	handler = middleware.StoreMiddleware(storeResolver)(handler)
	handler = middleware.LoggingMiddleware(handler)
	handler = middleware.CORSMiddleware(handler)
	handler = middleware.RecoveryMiddleware(handler)

	// HTTP server
	server := &http.Server{
		Addr:         ":" + a.cfg.Server.Port,
		Handler:      handler,
		ReadTimeout:  a.cfg.Server.ReadTimeout,
		WriteTimeout: a.cfg.Server.WriteTimeout,
		IdleTimeout:  120 * time.Second,
	}

	// Graceful shutdown
	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Info().Str("port", a.cfg.Server.Port).Msg("server starting")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("server failed")
		}
	}()

	<-done
	log.Info().Msg("shutting down...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		return fmt.Errorf("shutdown failed: %w", err)
	}

	a.db.Close()
	if a.cache != nil {
		a.cache.Close()
	}
	log.Info().Msg("server stopped")
	return nil
}
