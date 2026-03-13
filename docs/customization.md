# Customization & Extension Guide

This service is built with clear separation of concerns, making it straightforward to customize for your specific Magento setup or extend with additional functionality.

## Project Structure (Quick Reference)

```
graph/
  schema.graphqls         ← GraphQL schema (add fields/types here)
  schema.resolvers.go     ← Query resolver entry point
  resolver.go             ← Dependency wiring
  generated.go            ← Auto-generated (do not edit)
  model/models_gen.go     ← Auto-generated Go types (do not edit)
internal/
  repository/             ← One file per data domain (SQL queries)
  service/products.go     ← Query orchestration and type mapping
  service/fields.go       ← GraphQL AST field detection
  middleware/             ← HTTP middleware (CORS, caching, store, etc.)
```

## Common Customizations

### Adding a New Product Attribute

If your Magento instance has custom EAV attributes you want to expose in GraphQL:

**1. Add the field to the GraphQL schema** (`graph/schema.graphqls`):

```graphql
interface ProductInterface {
    # ... existing fields ...
    my_custom_attribute: String
}
```

**2. Regenerate Go types:**

```bash
go run github.com/99designs/gqlgen generate
```

This updates `graph/generated.go` and `graph/model/models_gen.go`.

**3. Add the attribute to the EAV query** (`internal/repository/product.go`):

The `ProductEAVValues` struct holds all flattened EAV values. Add your field:

```go
type ProductEAVValues struct {
    // ... existing fields ...
    MyCustomAttribute *string
}
```

Then add it to the `FindProducts()` query builder. The attribute must exist in the `eav_attribute` table — the `AttributeRepository` cache (loaded at startup) provides its `attribute_id` and `backend_type`, which determines which EAV value table to JOIN (`catalog_product_entity_varchar`, `_int`, `_text`, `_decimal`, or `_datetime`).

**4. Map to the GraphQL model** (`internal/service/products.go`):

In the `mapProductToModel()` function, add the mapping:

```go
// Inside mapProductToModel
if p.MyCustomAttribute != nil {
    product.MyCustomAttribute = p.MyCustomAttribute
}
```

**5. Register for field-selective loading** (`internal/service/fields.go`):

If you want the attribute to only be loaded when the client requests it, add detection in `CollectRequestedFields()`. For simple EAV attributes that are always JOINed, this step is optional.

### Adding a New Filter

To support filtering by a custom attribute (e.g., `filter: { brand: { eq: "Nike" } }`):

**1. Add the filter input** (`graph/schema.graphqls`):

```graphql
input ProductAttributeFilterInput {
    # ... existing filters ...
    brand: FilterEqualTypeInput
}
```

**2. Regenerate:**

```bash
go run github.com/99designs/gqlgen generate
```

**3. Handle the filter in the product query** (`internal/repository/product.go`):

In the `FindProducts()` method, add a clause for your new filter. The existing filters (sku, name, url_key, category_id, price) serve as patterns:

```go
if filter.Brand != nil && filter.Brand.Eq != nil {
    // JOIN the appropriate EAV table for this attribute
    attrMeta := r.attrRepo.GetByCode("brand")
    table := "catalog_product_entity_" + attrMeta.BackendType
    alias := "brand_filter"
    joins = append(joins, fmt.Sprintf(
        "INNER JOIN %s %s ON e.row_id = %s.row_id AND %s.attribute_id = %d AND %s.store_id IN (0, ?) AND %s.value = ?",
        table, alias, alias, alias, attrMeta.AttributeID, alias, alias,
    ))
}
```

### Adding a New Aggregation (Facet)

Aggregations power the layered navigation in Magento storefronts. To add a new facet:

**1.** Edit `internal/repository/aggregation.go`. The existing `GetAggregations()` method builds aggregations for categories, price ranges, and select-type attributes. Add your custom aggregation logic following the same patterns.

**2.** The aggregation is automatically included if it matches a filterable attribute in `eav_attribute` with `is_filterable = 1`. For custom aggregations that don't follow this pattern, add them explicitly.

### Adding a New GraphQL Query

To add a completely new root query (e.g., `categoryList`):

**1. Define the query in the schema** (`graph/schema.graphqls`):

```graphql
type Query {
    products(...): Products
    categoryList(filters: CategoryFilterInput): [CategoryTree]
}
```

**2. Regenerate:**

```bash
go run github.com/99designs/gqlgen generate
```

**3. Implement the resolver** (`graph/schema.resolvers.go`):

gqlgen will generate a stub. Implement it by calling a new service method:

```go
func (r *queryResolver) CategoryList(ctx context.Context, filters *model.CategoryFilterInput) ([]*model.CategoryTree, error) {
    return r.CategoryService.GetCategories(ctx, filters)
}
```

**4. Create the service and repository:**

Follow the existing patterns — create `internal/repository/category_list.go` for SQL queries and `internal/service/categories.go` for business logic. Wire them through `graph/resolver.go`.

### Switching to Magento Community Edition (Open Source)

This service is built for Magento Enterprise Edition (Adobe Commerce), which uses `row_id` as the surrogate key in EAV value tables. Magento Community Edition uses `entity_id` instead.

To adapt:

**1.** In `internal/repository/product.go`, change all EAV value table JOINs from:

```sql
LEFT JOIN catalog_product_entity_varchar ... ON e.row_id = v.row_id
```

to:

```sql
LEFT JOIN catalog_product_entity_varchar ... ON e.entity_id = v.entity_id
```

**2.** Make the same change in `internal/repository/configurable.go`, `bundle.go`, and any other repository that JOINs EAV value tables.

**3.** The `ProductEAVValues.RowID` field can be removed or kept as an alias for `entity_id`.

### Customizing the Middleware Stack

The middleware chain is defined in `internal/app/app.go`:

```go
var handler http.Handler = mux
handler = middleware.CacheMiddleware(a.cache)(handler)
handler = middleware.StoreMiddleware(storeResolver)(handler)
handler = middleware.LoggingMiddleware(handler)
handler = middleware.CORSMiddleware(handler)
handler = middleware.RecoveryMiddleware(handler)
```

Each middleware is a standard `func(http.Handler) http.Handler`. You can:

- **Remove caching**: Remove the `CacheMiddleware` line (or set `REDIS_HOST` to empty)
- **Add authentication**: Insert your own middleware before the GraphQL handler
- **Add rate limiting**: Add a rate limiter middleware after CORS
- **Customize CORS**: Edit `internal/middleware/cors.go` to restrict origins
- **Add metrics**: Add Prometheus middleware for request metrics

Example — adding a simple API key middleware:

```go
func APIKeyMiddleware(validKey string) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            if r.Header.Get("X-API-Key") != validKey {
                http.Error(w, "unauthorized", http.StatusUnauthorized)
                return
            }
            next.ServeHTTP(w, r)
        })
    }
}
```

### Customizing the Cache

The built-in Redis cache uses a 5-minute TTL with SHA256-hashed keys. To customize:

- **Change TTL**: Edit the `cacheTTL` constant in `internal/middleware/cache.go`
- **Change key strategy**: Edit the key generation in the same file
- **Use a different cache backend**: Implement the same interface with Memcached, in-memory LRU, etc.
- **Per-query TTL**: Modify the cache middleware to set different TTLs based on the query type

### Adding Custom Logging

The service uses [zerolog](https://github.com/rs/zerolog) for structured JSON logging. To add custom log fields:

```go
import "github.com/rs/zerolog/log"

// In any function
log.Info().
    Str("sku", product.SKU).
    Int("store_id", storeID).
    Dur("query_time", elapsed).
    Msg("product loaded")
```

## Development Workflow

### Schema Changes

After editing `graph/schema.graphqls`:

```bash
# Regenerate Go types and resolver stubs
go run github.com/99designs/gqlgen generate

# Build to verify
go build ./...
```

### Testing Changes

```bash
# Run integration tests (requires MySQL)
TEST_DB_NAME=magento TEST_DB_HOST=localhost go test ./tests/ -v -timeout 60s

# Run comparison tests (requires both Go service and Magento running)
GO_GRAPHQL_URL=http://localhost:8080/graphql \
MAGE_GRAPHQL_URL=http://your-magento.local/graphql \
go test ./tests/ -run TestCompare -v -timeout 300s
```

### Adding a New Repository

1. Create `internal/repository/your_domain.go`
2. Follow the existing pattern: struct with `*sql.DB`, constructor, and query methods
3. Wire it in `graph/resolver.go` → pass to `service.NewProductService()` (or a new service)
4. If the data should only be loaded when requested, add field detection in `internal/service/fields.go`
