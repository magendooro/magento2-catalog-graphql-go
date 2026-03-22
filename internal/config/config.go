package config

import (
	"strings"
	"time"

	"github.com/spf13/viper"
)

type Config struct {
	Server   ServerConfig   `mapstructure:"server"`
	Database DatabaseConfig `mapstructure:"database"`
	Redis    RedisConfig    `mapstructure:"redis"`
	GraphQL  GraphQLConfig  `mapstructure:"graphql"`
	Media    MediaConfig    `mapstructure:"media"`
	Logging  LoggingConfig  `mapstructure:"logging"`
}

type ServerConfig struct {
	Port         string        `mapstructure:"port"`
	ReadTimeout  time.Duration `mapstructure:"read_timeout"`
	WriteTimeout time.Duration `mapstructure:"write_timeout"`
}

type DatabaseConfig struct {
	Host            string        `mapstructure:"host"`
	Port            string        `mapstructure:"port"`
	User            string        `mapstructure:"user"`
	Password        string        `mapstructure:"password"`
	Name            string        `mapstructure:"name"`
	MaxOpenConns    int           `mapstructure:"max_open_conns"`
	MaxIdleConns    int           `mapstructure:"max_idle_conns"`
	ConnMaxLifetime time.Duration `mapstructure:"conn_max_lifetime"`
	ConnMaxIdleTime time.Duration `mapstructure:"conn_max_idle_time"`
}

type RedisConfig struct {
	Host     string `mapstructure:"host"`
	Port     string `mapstructure:"port"`
	Password string `mapstructure:"password"`
	DB       int    `mapstructure:"db"`
}

type GraphQLConfig struct {
	ComplexityLimit int `mapstructure:"complexity_limit"`
	MaxDepth        int `mapstructure:"max_depth"`
}

type MediaConfig struct {
	BaseURL   string `mapstructure:"base_url"`
	CacheHash string `mapstructure:"cache_hash"`
}

type LoggingConfig struct {
	Level  string `mapstructure:"level"`
	Pretty bool   `mapstructure:"pretty"`
}

func Load() (*Config, error) {
	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")
	viper.AddConfigPath("./config")
	viper.AddConfigPath("/config")

	// Defaults
	viper.SetDefault("server.port", "8080")
	viper.SetDefault("server.read_timeout", "30s")
	viper.SetDefault("server.write_timeout", "30s")
	viper.SetDefault("database.host", "localhost")
	viper.SetDefault("database.port", "3306")
	viper.SetDefault("database.user", "root")
	viper.SetDefault("database.name", "magento")
	viper.SetDefault("database.max_open_conns", 25)
	viper.SetDefault("database.max_idle_conns", 10)
	viper.SetDefault("database.conn_max_lifetime", "5m")
	viper.SetDefault("database.conn_max_idle_time", "1m")
	viper.SetDefault("redis.host", "127.0.0.1")
	viper.SetDefault("redis.port", "6379")
	viper.SetDefault("redis.db", 0)
	viper.SetDefault("graphql.complexity_limit", 1000)
	viper.SetDefault("graphql.max_depth", 15)
	viper.SetDefault("logging.level", "info")
	viper.SetDefault("logging.pretty", false)

	// Environment variable overrides: DB_HOST, DB_PORT, DB_USER, DB_PASSWORD, DB_NAME, etc.
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv()

	// Explicit bindings for common env vars (allows shorter names)
	viper.BindEnv("database.host", "DB_HOST")
	viper.BindEnv("database.port", "DB_PORT")
	viper.BindEnv("database.user", "DB_USER")
	viper.BindEnv("database.password", "DB_PASSWORD")
	viper.BindEnv("database.name", "DB_NAME")
	viper.BindEnv("redis.host", "REDIS_HOST")
	viper.BindEnv("redis.port", "REDIS_PORT")
	viper.BindEnv("redis.password", "REDIS_PASSWORD")
	viper.BindEnv("redis.db", "REDIS_DB")
	viper.BindEnv("server.port", "SERVER_PORT", "PORT")
	viper.BindEnv("media.base_url", "MEDIA_BASE_URL")
	viper.BindEnv("media.cache_hash", "MEDIA_CACHE_HASH")
	viper.BindEnv("logging.level", "LOG_LEVEL")
	viper.BindEnv("logging.pretty", "LOG_PRETTY")

	// Config file is optional — env vars and defaults are sufficient
	_ = viper.ReadInConfig()

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}
