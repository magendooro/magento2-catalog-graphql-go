# magento2-catalog-graphql-go

High-performance Go drop-in replacement for Magento 2's `products()` GraphQL query. Reads directly from Magento's MySQL database, delivering identical results at 7-18x the speed of PHP. Supports all product types, EAV attributes, pricing, inventory, categories, URL rewrites, reviews, aggregations, and multi-store scoping via gqlgen.

## Quick Start

### Binary

```bash
git clone https://github.com/magendooro/magento2-catalog-graphql-go.git
cd magento2-catalog-graphql-go
go build -o server ./cmd/server/

DB_HOST=localhost DB_NAME=magento ./server
```

### Docker

```bash
docker run -p 8080:8080 \
  -e DB_HOST=host.docker.internal \
  -e DB_NAME=magento \
  magento2-catalog-graphql
```

### Docker Compose

```bash
DB_HOST=host.docker.internal DB_NAME=magento docker compose up
```

Endpoints: GraphQL at `/graphql`, Playground at `/`, Health at `/health`.

## Configuration

All settings can be provided via **environment variables**, a **config file**, or **built-in defaults**. No config file is required.

| Variable | Default | Description |
|----------|---------|-------------|
| `DB_HOST` | `localhost` | MySQL host |
| `DB_PORT` | `3306` | MySQL port |
| `DB_USER` | `root` | MySQL user |
| `DB_PASSWORD` | `""` | MySQL password |
| `DB_NAME` | `magento` | Magento database name |
| `REDIS_HOST` | `127.0.0.1` | Redis host (empty to disable) |
| `SERVER_PORT` | `8080` | HTTP listen port |
| `MEDIA_BASE_URL` | `""` | Media URL (empty = auto-detect from DB) |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |

For config file usage, copy `config/config.yaml.example` to `config/config.yaml`.

See [docs/setup.md](docs/setup.md) for the full setup guide including database user creation, multi-store configuration, and production tuning.

## Features

- **All 5 product types**: Simple, Configurable, Bundle, Virtual, Grouped
- **68 fields implemented** across ProductInterface, pricing, media, inventory, categories, URLs, reviews
- **7 filter types**: sku, name, url_key, category_id, category_uid, category_url_path, price
- **Full-text search** with search suggestions
- **Aggregations** (faceted navigation): category, price, select attributes
- **Store-scoped multi-tenancy** via `Store` HTTP header
- **Field-selective batch loading**: only queries data the client requested
- **Redis response caching** with 5-minute TTL (optional)
- **GraphQL Playground** at `/` for development

## Magento Compatibility

- **Magento 2.4+ Enterprise Edition**: Uses `row_id` for EAV value table JOINs
- **Read-only**: Does not write to the database
- **Same database**: Reads from the same MySQL instance as Magento

23 out of 26 comparison tests produce **field-by-field identical** results to Magento PHP.

## Performance

| Query Pattern | Go | Magento PHP | Speedup |
|--------------|-----|-------------|---------|
| SKU lookup | 20ms | 143ms | **7.1x** |
| Category filter | 15ms | 127ms | **8.7x** |
| Full product (65+ fields) | 18ms | 310ms | **17.7x** |
| Multi-SKU sorted | 14ms | 151ms | **10.4x** |

## Documentation

| Document | Description |
|----------|-------------|
| [docs/setup.md](docs/setup.md) | Installation, configuration, deployment |
| [docs/architecture.md](docs/architecture.md) | Architecture, design decisions, project structure |
| [docs/FEATURES_AND_TESTS.md](docs/FEATURES_AND_TESTS.md) | Feature list, test coverage matrix, root-cause analysis |

## License

MIT
