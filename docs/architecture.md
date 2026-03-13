# Architecture

## Overview

The service is a stateless, read-only GraphQL server that reads directly from Magento 2's MySQL database. It replaces Magento's PHP `products()` query resolver with a Go implementation that produces identical results at 7-18x the speed.

## Request Flow

```
HTTP Request
    |
    v
RecoveryMiddleware    -- catches panics, returns 500 with stack trace
    |
    v
CORSMiddleware        -- handles preflight, allows all origins
    |
    v
LoggingMiddleware     -- structured request/response logging (zerolog)
    |
    v
StoreMiddleware       -- resolves Store header to store_id via MySQL
    |
    v
CacheMiddleware       -- Redis response cache (SHA256 key, 5min TTL)
    |
    v
GraphQL Handler       -- gqlgen (schema-first, code-generated)
    |
    v
ProductService.GetProducts()
    |
    +-- 1. FindProducts()          -- EAV JOINs, filters, sorting, pagination
    +-- 2. CollectRequestedFields()-- inspect GraphQL AST for needed sub-fields
    +-- 3. Batch load (parallel):
    |       +-- PriceRepository         (catalog_product_index_price)
    |       +-- MediaRepository         (catalog_product_entity_media_gallery)
    |       +-- InventoryRepository     (cataloginventory_stock_item)
    |       +-- CategoryRepository      (catalog_category_product + EAV)
    |       +-- URLRepository           (url_rewrite)
    |       +-- ConfigurableRepository  (super_attribute + super_link + EAV)
    |       +-- BundleRepository        (bundle_option + bundle_selection)
    |       +-- ProductLinkRepository   (catalog_product_link)
    |       +-- ReviewRepository        (review + review_detail + rating_option_vote)
    |       +-- AggregationRepository   (category/price/select facets)
    +-- 4. Map to GraphQL types    -- mapProductToModel per product type
    +-- 5. Return Products{}       -- items, page_info, aggregations, sort_fields
```

## Project Structure

```
cmd/server/              Entry point (main.go)
config/                  Configuration files
  config.yaml.example    Template config
docs/                    Documentation
  architecture.md        This file
  setup.md               Installation & setup guide
  FEATURES_AND_TESTS.md  Feature list & test coverage matrix
graph/                   GraphQL layer (gqlgen)
  schema.graphqls        GraphQL schema definition
  generated.go           Auto-generated resolvers and marshalers
  model/models_gen.go    Auto-generated Go types
  resolver.go            Root resolver with dependency wiring
  schema.resolvers.go    Query resolver implementation
internal/
  app/                   Application bootstrap, HTTP server, graceful shutdown
  cache/                 Redis client wrapper
  config/                Configuration types, loader (YAML + env vars)
  database/              MySQL connection setup (DSN, pooling, timezone)
  middleware/            HTTP middleware stack
    cache.go             Response caching
    cors.go              CORS headers
    logging.go           Request logging
    recovery.go          Panic recovery
    store.go             Store code -> store_id resolution
  repository/            Data access layer (one file per domain)
    aggregation.go       Faceted navigation (category, price, select)
    attribute.go         EAV attribute metadata cache
    bundle.go            Bundle product options and selections
    category.go          Product-to-category mapping, breadcrumbs
    configurable.go      Configurable variants, super attributes, swatches
    inventory.go         Stock status, quantities
    media.go             Media gallery, video support
    price.go             Price index, tier prices, discounts
    product.go           Core product query (EAV JOINs, filters, sorting)
    product_link.go      Related, upsell, crosssell links
    review.go            Review summaries and details
    search.go            Search suggestions from search_query table
    store.go             Store config cache (currency, URLs, thresholds)
    url.go               URL rewrites, canonical URLs
  service/               Business logic
    fields.go            GraphQL AST field detection
    products.go          Query orchestration, type mapping, batch loading
```

## Key Design Decisions

### EAV Resolution via Dynamic JOINs

Rather than N+1 queries per attribute, the product query builds a single SQL statement with LEFT JOINs per EAV attribute, using `COALESCE(store_value, default_value)` for store scoping. The attribute list is loaded once at startup from `eav_attribute` and cached.

### Field-Selective Batch Loading

`CollectRequestedFields()` inspects the GraphQL AST before any database work. If the client only requests `sku` and `name`, the service skips price index, media gallery, inventory, category, URL rewrite, and review queries entirely.

### Magento Enterprise Edition (row_id)

Magento EE uses `row_id` (auto-increment surrogate key) instead of `entity_id` (business key) in EAV value tables. All JOINs use `row_id` for value lookups. `entity_id` is used for external references (UIDs, price index, inventory).

### MySQL Timezone Handling

Magento's PHP sets `SET time_zone = '+00:00'` on every MySQL connection to get UTC timestamps. The Go service does the same via the DSN parameter `time_zone=%27%2B00%3A00%27`, ensuring `TIMESTAMP` columns return identical values.

### Response Caching

SHA256 hash of `store_code + request_body` as the cache key ensures different store views and different queries get separate cache entries. The 5-minute TTL is a reasonable default for catalog data that changes infrequently.

## Performance

The Go service is consistently 7-18x faster than Magento's PHP implementation:

| Query Pattern | Go | Magento PHP | Speedup |
|--------------|-----|-------------|---------|
| SKU lookup | 20ms | 143ms | **7.1x** |
| Category filter | 15ms | 127ms | **8.7x** |
| Full product (65+ fields) | 18ms | 310ms | **17.7x** |
| Multi-SKU sorted | 14ms | 151ms | **10.4x** |

Key reasons:
- **No framework overhead**: No DI container, plugin interceptors, or event system
- **Direct SQL**: Queries MySQL directly vs Magento's ORM stack
- **Field-selective loading**: Only queries data the client requested
- **Connection pooling**: Persistent pool vs PHP's per-request connections
- **Compiled binary**: Native code vs interpreted PHP
