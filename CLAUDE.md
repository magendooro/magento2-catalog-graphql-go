# Project: magento2-catalog-graphql-go

High-performance Go drop-in replacement for Magento 2's `products()` GraphQL query using gqlgen. Produces identical responses to Magento PHP for 24 of 26 tested query patterns.

## Architecture

- **Schema-first GraphQL** via gqlgen — edit `graph/schema.graphqls`, then `GOTOOLCHAIN=auto go run github.com/99designs/gqlgen generate`
- **Never edit** `graph/generated.go` or `graph/model/models_gen.go` — they are auto-generated
- **Community Edition** — all EAV JOINs use `entity_id`. See "Enterprise Edition Support" below for `row_id` plan.
- **Mostly read-only** — the only write operation is `createProductReview` (inserts into `review`, `review_detail`, `review_store`, `rating_option_vote`)
- **Unix socket DB** — when `DB_HOST=localhost`, connects via `/tmp/mysql.sock`

## Current State (April 2026)

### What works (24/26 identical to Magento PHP)
- Simple/multi SKU lookup, name filter, price range, category filter
- Categories with url_key/url_path (dynamic attribute ID subqueries)
- URL rewrites, stock status, pagination, sorting
- Bundle products (items, options, selections)
- Configurable products (options sorted alphabetically, variants with child SKUs)
- Aggregations / faceted navigation (identical to Magento PHP)
- `categories` query (paginated category tree)
- `categoryList` query (flat tree list)
- `route` query (implemented in store service)
- Empty result handling

### Known differences (2/26)
- **Media gallery URLs** (×2 tests): Magento returns PHP-generated cache URLs (`/cache/{hash}/path.jpg`) while Go returns the raw database path (`/media/catalog/product/path.jpg`). Both point to the same file. Difference is cosmetic — storefront uses `buildImageUrl()` which constructs media service URLs regardless.

> **Reviews**: Was previously listed as a known difference ("Magento PHP bug"). Investigation revealed three real bugs in Go: `created_at` RFC3339 format, `pageSize`/`currentPage` arguments ignored, `total_pages` always 1. All three fixed April 2026. Now identical to Magento.

> **Search**: Uses same OpenSearch index Magento builds (`magento2_product_1`). Falls back to MySQL `LIKE` only when OpenSearch is unreachable.

## Project Structure

```
cmd/server/           Entry point
graph/                GraphQL schema, resolvers, generated code
internal/
  app/                HTTP server bootstrap (uses magento2-go-common for db/cache/middleware)
  config/             Config loader — Viper struct only (env vars > YAML > defaults)
                      NOTE: ConfigProvider comes from magento2-go-common/config
  repository/         Data access — one file per domain:
    product.go        Product query with dynamic EAV JOINs
    attribute.go      EAV attribute metadata cache (loaded at startup)
    category.go       Category EAV (dynamic attribute IDs, not hardcoded)
    configurable.go   Super attributes, super links, child products
    bundle.go         Bundle options and selections
    media.go          Media gallery
    price.go          Price index + tier prices
    inventory.go      Stock status
    url.go            URL rewrites
    review.go         Review summaries
    aggregation.go    Faceted navigation (category, price, select attributes)
    search.go         Search suggestions
    store.go          Store config (uses magento2-go-common/config.ConfigProvider)
    product_link.go   Related/upsell/crosssell
  search/             OpenSearch/Elasticsearch client
  service/
    products.go       Query orchestration, parallel batch loading, type mapping
    fields.go         GraphQL AST field detection (CollectRequestedFields)
tests/                Integration + comparison tests (HTTP-based via httptest)
```

### Shared packages (from magento2-go-common)

`internal/cache`, `internal/database`, and `internal/middleware` were removed — they are provided by `magento2-go-common`. `graph/resolver.go` uses aliased imports:

```go
localconfig  "github.com/magendooro/magento2-catalog-graphql-go/internal/config"  // Viper struct
commonconfig "github.com/magendooro/magento2-go-common/config"                    // ConfigProvider
```

## Build & Test

```bash
GOTOOLCHAIN=auto go build -o server ./cmd/server/    # build
GOTOOLCHAIN=auto go vet ./...                         # lint
GOTOOLCHAIN=auto go run github.com/99designs/gqlgen generate  # regenerate

# integration tests (needs MySQL with Magento DB)
GOTOOLCHAIN=auto go test ./tests/ -v -timeout 60s -count=1

# run server (port 8083 to avoid Magento on 8080)
DB_HOST=localhost DB_USER=fch DB_NAME=magento248 SERVER_PORT=8083 GOTOOLCHAIN=auto go run ./cmd/server/

# live comparison (both services must be running)
# Magento at :8080, Go at :8083 — send same query to both and diff JSON
```

Test env vars: `TEST_DB_HOST` (default: localhost), `TEST_DB_USER` (default: fch), `TEST_DB_NAME` (default: magento248), `TEST_DB_SOCKET` (default: /tmp/mysql.sock).

## Key Conventions

- **Go 1.25** — use `GOTOOLCHAIN=auto` for all commands
- **Error handling**: `fmt.Errorf("context: %w", err)`, use `errors.Is`/`errors.As`
- **Naming**: `CamelCase` exported, `camelCase` unexported, no stutter
- **Logging**: zerolog structured JSON — `log.Info().Str("key", val).Msg("message")`
- **Context**: always first parameter `ctx context.Context`
- **EAV attributes**: loaded once at startup into `AttributeRepository`, never hardcode attribute IDs
- **Category attributes**: use subqueries `(SELECT attribute_id FROM eav_attribute WHERE attribute_code = '...' AND entity_type_id = 3)` — never hardcode IDs
- **Field-selective loading**: `CollectRequestedFields()` inspects GraphQL AST. Pass all product type names when collecting fields inside fragments.
- **SQL safety**: DESCRIBE tables before writing queries, COALESCE nullable numerics, whitelist identifiers

## Lessons Learned

### CE vs EE
The original code used `row_id` (Enterprise Edition only). CE uses `entity_id` everywhere. The fix was a global find-replace plus fixing:
- `catalog_product_super_link` has no `position` column in CE — use `link_id`
- Selecting `cpe.entity_id, cpe.entity_id` (was `entity_id, row_id`) works because both are the same value in CE

### Hardcoded attribute IDs break across installations
Category attributes had IDs 45/124/125/46/47 hardcoded from one specific installation. The actual IDs vary. Fix: use subqueries against `eav_attribute`.

### CollectFields requires type names for fragments
`graphql.CollectFields(opCtx, selections, nil)` won't collect fields inside `... on ConfigurableProduct {}` fragments. Must pass `[]string{"SimpleProduct", "ConfigurableProduct", ...}` as the type satisfies parameter.

### Redis cache returns stale data after code changes
After changing response format (e.g., fixing category url_key), Redis still serves the old cached response. Always `redis-cli FLUSHALL` when testing fixes.

### Configurable options sort order
Magento sorts `configurable_options` alphabetically by `attribute_code`, not by the DB `position` field.

### Scan column count must match SELECT column count
When `GetChildProductsEAV` or any function using `coreEAVAttributes` adds a new EAV attribute to the SELECT, the `rows.Scan(...)` call must receive the same number of destination pointers. Mismatches cause a silent `"expected N destination arguments in Scan, not M"` error that returns empty/null data without crashing. When adding a new EAV attribute: update both the `coreEAVAttributes` slice AND every `Scan()` call that uses it.

## Search architecture

The catalog service reads Magento's OpenSearch index — it does **not** build or maintain the index itself.

```
Magento indexer (catalogsearch_fulltext) ──writes──▶ magento2_product_1 (OpenSearch)
                                                             ↑
                                         Go catalog service ──reads──┘
```

**At startup:** probes `http://localhost:9200/`. If reachable → use OpenSearch queries. If not → fall back to MySQL `LIKE`.

**Query when OpenSearch available:**
- `match(_search: term)` — full-text over all searchable attributes
- `match(sku: term)` boosted ×2
- `match_phrase_prefix(name: term)` — autocomplete

**Fallback (OpenSearch down):** `WHERE sku LIKE '%term%' OR name LIKE '%term%' OR description LIKE '%term%' ...` — no relevance ranking.

**After OpenSearch restarts**, restart the catalog service too (availability checked once at startup):
```bash
./stop.sh catalog && ./start.sh --no-magento
```

**After product changes**, reindex:
```bash
cd ~/magento248 && php -d memory_limit=512M bin/magento indexer:reindex catalogsearch_fulltext
```

## Common Patterns

### Adding an EAV attribute
1. Add field to `graph/schema.graphqls` → regenerate
2. Add to `ProductEAVValues` struct in `internal/repository/product.go`
3. Add EAV JOIN in `FindProducts()` — the `AttributeRepository` provides the attribute_id
4. Map in `internal/service/products.go` `mapProductToModel()`

### Adding a filter
1. Add to `ProductAttributeFilterInput` in schema → regenerate
2. Handle in `FindProducts()` in `internal/repository/product.go`

### Adding a new root query
1. Add query + types to schema → regenerate
2. Create `internal/repository/<domain>.go`
3. Wire in `graph/resolver.go`
4. Implement resolver in `graph/schema.resolvers.go`
