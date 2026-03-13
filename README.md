# magento2-catalog-graphql-go

High-performance Go drop-in replacement for Magento 2's `products()` GraphQL query. Reads directly from Magento's MySQL database, delivering identical results at 7-18x the speed of PHP. Supports all product types, EAV attributes, pricing, inventory, categories, URL rewrites, reviews, aggregations, and multi-store scoping via gqlgen.

## Architecture

```
HTTP Request
    |
    v
Recovery -> CORS -> Logging -> Store -> Cache -> GraphQL (gqlgen)
    |
    v
ProductService.GetProducts()
    |
    +-- ProductRepository       (EAV JOINs, filters, sorting, pagination)
    +-- PriceRepository         (price_range, tier_prices)
    +-- MediaRepository         (media_gallery, images)
    +-- InventoryRepository     (stock_status, qty)
    +-- CategoryRepository      (product->category mapping, breadcrumbs)
    +-- URLRepository           (url_rewrites, canonical_url)
    +-- ConfigurableRepository  (variants, options, swatches)
    +-- BundleRepository        (bundle items, options, dynamic pricing)
    +-- ProductLinkRepository   (related/upsell/crosssell)
    +-- AggregationRepository   (faceted filters)
    +-- ReviewRepository        (ratings, reviews)
    +-- SearchRepository        (search suggestions)
    +-- StoreConfigRepository   (currency, base URL, thresholds)
```

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

## Requirements

- Go 1.23+
- MySQL 5.7+ / MariaDB 10.3+ (Magento 2 database, read-only access)
- Redis (optional, for response caching)

## Quick Start

1. **Clone and build**
```bash
git clone https://github.com/magendooro/magento2-catalog-graphql-go.git
cd magento2-catalog-graphql-go
go build -o server ./cmd/server/
```

2. **Configure**
```bash
cp config/config.yaml.example config/config.yaml
# Edit config/config.yaml with your Magento database credentials
```

3. **Run**
```bash
./server
# GraphQL endpoint: http://localhost:8080/graphql
# Playground:       http://localhost:8080/
# Health check:     http://localhost:8080/health
```

## Docker

```bash
docker build -t magento2-catalog-graphql .
docker run -p 8080:8080 \
  -v $(pwd)/config/config.yaml:/config/config.yaml \
  magento2-catalog-graphql
```

## Configuration

All configuration is via `config/config.yaml`. See `config/config.yaml.example` for defaults.

| Section | Key | Default | Description |
|---------|-----|---------|-------------|
| `server.port` | | `8080` | HTTP listen port |
| `database.host` | | `localhost` | MySQL host |
| `database.name` | | `magento` | Magento database name |
| `redis.host` | | `127.0.0.1` | Redis host (optional) |
| `media.base_url` | | `""` (auto-detect) | Product media base URL; empty = read from `core_config_data` |
| `graphql.complexity_limit` | | `1000` | GraphQL query complexity limit |
| `graphql.max_depth` | | `15` | Maximum query depth |

Environment variables can override config values. Integration tests use `TEST_DB_*` env vars.

## Magento Compatibility

- **Magento 2.4+ (Enterprise Edition)**: Uses `row_id` for EAV value table JOINs
- **Read-only access**: Does not write to the database
- **Same database**: Reads from the same MySQL database as Magento for guaranteed consistency
- **Store scoping**: Pass `Store: <store_code>` header for multi-store setups

### Verified Identical

23 out of 26 comparison tests produce **field-by-field identical** results to Magento PHP. The 3 remaining differences are not bugs:
- **Image placeholders**: Magento checks filesystem existence; Go returns the database URL (both match in production where files exist)
- **Reviews**: Magento has a PHP bug with `ProductReviews.page_info` returning null on non-nullable field

See [FEATURES_AND_TESTS.md](FEATURES_AND_TESTS.md) for the full test coverage matrix and root-cause analysis.

## Performance

| Query Pattern | Go | Magento PHP | Speedup |
|--------------|-----|-------------|---------|
| SKU lookup | 20ms | 143ms | **7.1x** |
| Category filter | 15ms | 127ms | **8.7x** |
| Full product (65+ fields) | 18ms | 310ms | **17.7x** |
| Multi-SKU sorted | 14ms | 151ms | **10.4x** |

## Testing

**Integration tests** (require MySQL with Magento database):
```bash
TEST_DB_NAME=magento go test -run 'Test(Products|Bundle|Configurable|Search|Health|Store|Swatch|Only|Empty|Review|Category|Price)' -v
```

**Comparison tests** (require both Go service and Magento running):
```bash
GO_GRAPHQL_URL=http://localhost:8080/graphql \
MAGE_GRAPHQL_URL=http://your-magento.local/graphql \
go test -run TestCompare -v -timeout 300s
```

## Project Structure

```
cmd/server/          Entry point
config/              Configuration files
graph/               GraphQL schema + generated code (gqlgen)
internal/
  app/               Application bootstrap, HTTP server
  cache/             Redis caching client
  config/            Configuration types and loader
  database/          MySQL connection setup
  middleware/        HTTP middleware (CORS, logging, store, cache, recovery)
  repository/        Data access layer (one file per domain)
  service/           Business logic (product query orchestration)
```

## License

MIT
