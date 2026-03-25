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

	"github.com/magendooro/magento2-catalog-graphql-go/graph"
	"github.com/magendooro/magento2-catalog-graphql-go/internal/repository"
	localconfig "github.com/magendooro/magento2-catalog-graphql-go/internal/config"
	commoncache "github.com/magendooro/magento2-go-common/cache"
	commondb "github.com/magendooro/magento2-go-common/database"
	"github.com/magendooro/magento2-go-common/middleware"
)

type App struct {
	cfg   *localconfig.Config
	db    *sql.DB
	cache *commoncache.Client
}

func New(cfg *localconfig.Config) (*App, error) {
	if cfg.Logging.Pretty {
		log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})
	}
	level, err := zerolog.ParseLevel(cfg.Logging.Level)
	if err != nil {
		level = zerolog.InfoLevel
	}
	zerolog.SetGlobalLevel(level)

	db, err := commondb.NewConnection(commondb.Config{
		Host:            cfg.Database.Host,
		Port:            cfg.Database.Port,
		User:            cfg.Database.User,
		Password:        cfg.Database.Password,
		Name:            cfg.Database.Name,
		// Catalog's DatabaseConfig has no Socket field; defaults to /tmp/mysql.sock for localhost
		MaxOpenConns:    cfg.Database.MaxOpenConns,
		MaxIdleConns:    cfg.Database.MaxIdleConns,
		ConnMaxLifetime: cfg.Database.ConnMaxLifetime,
		ConnMaxIdleTime: cfg.Database.ConnMaxIdleTime,
	})
	if err != nil {
		return nil, fmt.Errorf("database connection failed: %w", err)
	}
	log.Info().Str("database", cfg.Database.Name).Msg("connected to database")

	redisCache := commoncache.New(commoncache.Config{
		Host:     cfg.Redis.Host,
		Port:     cfg.Redis.Port,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
		Prefix:   "gql:",
	})

	return &App{cfg: cfg, db: db, cache: redisCache}, nil
}

func (a *App) Run() error {
	if hash := a.cfg.Media.CacheHash; hash != "" {
		repository.ImageCacheHash = hash
		log.Info().Str("hash", hash).Msg("media cache hash configured")
	}

	storeResolver := middleware.NewStoreResolver(a.db)

	resolver, err := graph.NewResolver(a.db, a.cfg)
	if err != nil {
		return fmt.Errorf("failed to create resolver: %w", err)
	}

	srv := handler.NewDefaultServer(graph.NewExecutableSchema(graph.Config{
		Resolvers: resolver,
	}))

	if a.cfg.GraphQL.ComplexityLimit > 0 {
		srv.Use(extension.FixedComplexityLimit(a.cfg.GraphQL.ComplexityLimit))
	}

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

	// Catalog caches all GraphQL responses — no auth, no mutations
	var h http.Handler = mux
	h = middleware.CacheMiddleware(a.cache, middleware.CacheOptions{SkipMutations: true})(h)
	h = middleware.StoreMiddleware(storeResolver)(h)
	h = middleware.LoggingMiddleware(h)
	h = middleware.CORSMiddleware(h)
	h = middleware.RecoveryMiddleware(h)

	server := &http.Server{
		Addr:         ":" + a.cfg.Server.Port,
		Handler:      h,
		ReadTimeout:  a.cfg.Server.ReadTimeout,
		WriteTimeout: a.cfg.Server.WriteTimeout,
		IdleTimeout:  120 * time.Second,
	}

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
