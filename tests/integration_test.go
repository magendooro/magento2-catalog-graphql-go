package tests

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/99designs/gqlgen/graphql/handler"
	_ "github.com/go-sql-driver/mysql"

	"github.com/magendooro/magento2-catalog-graphql-go/graph"
	"github.com/magendooro/magento2-catalog-graphql-go/internal/config"
	"github.com/magendooro/magento2-catalog-graphql-go/internal/middleware"
)

var testHandler http.Handler

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func TestMain(m *testing.M) {
	host := envOrDefault("TEST_DB_HOST", "localhost")
	cfg := &config.Config{
		Database: config.DatabaseConfig{
			Host:         host,
			Port:         envOrDefault("TEST_DB_PORT", "3306"),
			User:         envOrDefault("TEST_DB_USER", "magento_go"),
			Password:     envOrDefault("TEST_DB_PASSWORD", "magento_go"),
			Name:         envOrDefault("TEST_DB_NAME", "magento248"),
			MaxOpenConns: 5,
			MaxIdleConns: 2,
		},
	}

	var dsn string
	if host == "localhost" {
		socket := envOrDefault("TEST_DB_SOCKET", "/tmp/mysql.sock")
		dsn = cfg.Database.User + ":" + cfg.Database.Password + "@unix(" + socket + ")/" + cfg.Database.Name + "?parseTime=true"
	} else {
		dsn = cfg.Database.User + ":" + cfg.Database.Password + "@tcp(" + cfg.Database.Host + ":" + cfg.Database.Port + ")/" + cfg.Database.Name + "?parseTime=true"
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		panic("failed to connect to test database: " + err.Error())
	}
	if err := db.Ping(); err != nil {
		panic("failed to ping test database: " + err.Error())
	}

	resolver, err := graph.NewResolver(db, cfg)
	if err != nil {
		panic("failed to create resolver: " + err.Error())
	}

	srv := handler.NewDefaultServer(graph.NewExecutableSchema(graph.Config{
		Resolvers: resolver,
	}))

	storeResolver := middleware.NewStoreResolver(db)
	testHandler = middleware.StoreMiddleware(storeResolver)(srv)

	os.Exit(m.Run())
}

// graphqlRequest performs a GraphQL request and returns the parsed JSON response.
func graphqlRequest(t *testing.T, query string, store string) map[string]interface{} {
	t.Helper()

	body, _ := json.Marshal(map[string]string{"query": query})
	req := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if store != "" {
		req.Header.Set("Store", store)
	}

	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var result map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if errs, ok := result["errors"]; ok {
		t.Fatalf("GraphQL errors: %v", errs)
	}

	return result
}

// extractProducts extracts items from GraphQL products response.
func extractProducts(result map[string]interface{}) []interface{} {
	data := result["data"].(map[string]interface{})
	products := data["products"].(map[string]interface{})
	items, _ := products["items"].([]interface{})
	return items
}

func extractField(result map[string]interface{}, path ...string) interface{} {
	var current interface{} = result
	for _, key := range path {
		m, ok := current.(map[string]interface{})
		if !ok {
			return nil
		}
		current = m[key]
	}
	return current
}

// --- Tests ---

func TestProductsBySKU(t *testing.T) {
	result := graphqlRequest(t, `{
		products(filter: { sku: { eq: "B5247819" } }) {
			items { sku name uid type_id }
			total_count
		}
	}`, "default")

	items := extractProducts(result)
	if len(items) != 1 {
		t.Fatalf("expected 1 product, got %d", len(items))
	}

	item := items[0].(map[string]interface{})
	if item["sku"] != "B5247819" {
		t.Errorf("expected sku B5247819, got %v", item["sku"])
	}
	if item["name"] != "Bundle Aura" {
		t.Errorf("expected name 'Bundle Aura', got %v", item["name"])
	}
	if item["type_id"] != "bundle" {
		t.Errorf("expected type_id 'bundle', got %v", item["type_id"])
	}

	tc := extractField(result, "data", "products", "total_count")
	if tc != float64(1) {
		t.Errorf("expected total_count 1, got %v", tc)
	}
}

func TestProductsBySKUIn(t *testing.T) {
	result := graphqlRequest(t, `{
		products(filter: { sku: { in: ["B5247819", "A1358093"] } }) {
			items { sku }
			total_count
		}
	}`, "default")

	tc := extractField(result, "data", "products", "total_count")
	if tc != float64(2) {
		t.Errorf("expected total_count 2, got %v", tc)
	}
}

func TestProductsSearch(t *testing.T) {
	result := graphqlRequest(t, `{
		products(search: "aura", pageSize: 50) {
			items { sku name }
			total_count
		}
	}`, "default")

	items := extractProducts(result)
	if len(items) == 0 {
		t.Fatal("expected products for search 'aura'")
	}

	tc := extractField(result, "data", "products", "total_count").(float64)
	if tc < 1 {
		t.Errorf("expected at least 1 result, got %v", tc)
	}
}

func TestProductsPriceRange(t *testing.T) {
	result := graphqlRequest(t, `{
		products(filter: { sku: { eq: "B5247819" } }) {
			items {
				sku
				price_range {
					minimum_price {
						regular_price { value currency }
						final_price { value currency }
					}
					maximum_price {
						regular_price { value currency }
						final_price { value currency }
					}
				}
			}
		}
	}`, "default")

	items := extractProducts(result)
	if len(items) != 1 {
		t.Fatalf("expected 1 product, got %d", len(items))
	}

	item := items[0].(map[string]interface{})
	pr := item["price_range"].(map[string]interface{})
	minPrice := pr["minimum_price"].(map[string]interface{})
	finalPrice := minPrice["final_price"].(map[string]interface{})

	if finalPrice["currency"] != "AED" {
		t.Errorf("expected AED currency, got %v", finalPrice["currency"])
	}
	if finalPrice["value"].(float64) <= 0 {
		t.Errorf("expected positive price, got %v", finalPrice["value"])
	}
}

func TestProductsMediaGallery(t *testing.T) {
	result := graphqlRequest(t, `{
		products(filter: { sku: { eq: "B5247819" } }) {
			items {
				sku
				media_gallery { url label position disabled }
			}
		}
	}`, "default")

	items := extractProducts(result)
	item := items[0].(map[string]interface{})
	gallery := item["media_gallery"].([]interface{})

	if len(gallery) == 0 {
		t.Fatal("expected media gallery entries")
	}

	first := gallery[0].(map[string]interface{})
	url := first["url"].(string)
	if url == "" {
		t.Error("expected non-empty media URL")
	}
}

func TestProductsInventory(t *testing.T) {
	result := graphqlRequest(t, `{
		products(filter: { sku: { eq: "B5247819" } }) {
			items { sku stock_status }
		}
	}`, "default")

	items := extractProducts(result)
	item := items[0].(map[string]interface{})

	if item["stock_status"] != "IN_STOCK" && item["stock_status"] != "OUT_OF_STOCK" {
		t.Errorf("unexpected stock_status: %v", item["stock_status"])
	}
}

func TestProductsCategories(t *testing.T) {
	result := graphqlRequest(t, `{
		products(filter: { sku: { eq: "B5247819" } }) {
			items { sku categories { uid name } }
		}
	}`, "default")

	items := extractProducts(result)
	item := items[0].(map[string]interface{})
	cats := item["categories"].([]interface{})

	if len(cats) == 0 {
		t.Fatal("expected at least one category")
	}

	cat := cats[0].(map[string]interface{})
	if cat["name"] == nil || cat["name"] == "" {
		t.Error("expected category name")
	}
}

func TestProductsURLRewrites(t *testing.T) {
	result := graphqlRequest(t, `{
		products(filter: { sku: { eq: "B5247819" } }) {
			items { sku url_key url_rewrites { url } }
		}
	}`, "default")

	items := extractProducts(result)
	item := items[0].(map[string]interface{})

	if item["url_key"] == nil {
		t.Error("expected url_key")
	}

	rewrites := item["url_rewrites"].([]interface{})
	if len(rewrites) == 0 {
		t.Fatal("expected URL rewrites")
	}
}

func TestProductsPagination(t *testing.T) {
	result := graphqlRequest(t, `{
		products(pageSize: 3, currentPage: 1) {
			items { sku }
			total_count
			page_info { page_size current_page total_pages }
		}
	}`, "default")

	items := extractProducts(result)
	if len(items) != 3 {
		t.Errorf("expected 3 items, got %d", len(items))
	}

	pageInfo := extractField(result, "data", "products", "page_info").(map[string]interface{})
	if pageInfo["page_size"] != float64(3) {
		t.Errorf("expected page_size 3, got %v", pageInfo["page_size"])
	}
	if pageInfo["current_page"] != float64(1) {
		t.Errorf("expected current_page 1, got %v", pageInfo["current_page"])
	}
	if pageInfo["total_pages"].(float64) < 1 {
		t.Error("expected at least 1 total page")
	}
}

func TestProductsSorting(t *testing.T) {
	result := graphqlRequest(t, `{
		products(pageSize: 5, sort: { name: ASC }) {
			items { name }
		}
	}`, "default")

	items := extractProducts(result)
	if len(items) == 0 {
		t.Fatal("expected products")
	}

	var prev string
	for _, item := range items {
		name := item.(map[string]interface{})["name"].(string)
		if name < prev {
			t.Errorf("products not sorted by name ASC: %q after %q", name, prev)
		}
		prev = name
	}
}

func TestProductsCategoryFilter(t *testing.T) {
	result := graphqlRequest(t, `{
		products(filter: { sku: { eq: "B5247819" } }) {
			items { categories { uid name } }
		}
	}`, "default")

	items := extractProducts(result)
	cats := items[0].(map[string]interface{})["categories"].([]interface{})
	catUID := cats[0].(map[string]interface{})["uid"].(string)

	result2 := graphqlRequest(t, `{
		products(filter: { category_uid: { eq: "`+catUID+`" } }, pageSize: 50) {
			items { sku }
			total_count
		}
	}`, "default")

	tc := extractField(result2, "data", "products", "total_count").(float64)
	if tc < 1 {
		t.Errorf("expected at least 1 product in category, got %v", tc)
	}
}

func TestProductsAggregations(t *testing.T) {
	result := graphqlRequest(t, `{
		products(pageSize: 1) {
			aggregations { attribute_code label options { label value count } }
		}
	}`, "default")

	aggs := extractField(result, "data", "products", "aggregations").([]interface{})
	if len(aggs) == 0 {
		t.Fatal("expected aggregations")
	}

	codes := make(map[string]bool)
	for _, agg := range aggs {
		a := agg.(map[string]interface{})
		codes[a["attribute_code"].(string)] = true

		opts := a["options"].([]interface{})
		if len(opts) == 0 {
			t.Errorf("aggregation %s has no options", a["attribute_code"])
		}
	}

	if !codes["category_id"] {
		t.Error("expected category_id aggregation")
	}
	if !codes["price"] {
		t.Error("expected price aggregation")
	}
}

func TestProductsSortFields(t *testing.T) {
	result := graphqlRequest(t, `{
		products(pageSize: 1) {
			sort_fields { default options { value label } }
		}
	}`, "default")

	sf := extractField(result, "data", "products", "sort_fields").(map[string]interface{})
	if sf["default"] != "position" {
		t.Errorf("expected default sort 'position', got %v", sf["default"])
	}

	opts := sf["options"].([]interface{})
	if len(opts) < 3 {
		t.Errorf("expected at least 3 sort options, got %d", len(opts))
	}
}

func TestSearchSuggestions(t *testing.T) {
	result := graphqlRequest(t, `{
		products(search: "test", pageSize: 1) {
			suggestions { search }
		}
	}`, "default")

	suggs := extractField(result, "data", "products", "suggestions")
	if suggs == nil {
		t.Skip("no search suggestions in database — search_query table may be empty")
	}
}

func TestConfigurableProduct(t *testing.T) {
	result := graphqlRequest(t, `{
		products(pageSize: 50) {
			items {
				sku
				__typename
				... on ConfigurableProduct {
					variants { attributes { label code value_index } product { sku } }
					configurable_options { attribute_code label values { value_index label uid } }
				}
			}
		}
	}`, "default")

	items := extractProducts(result)
	var found bool
	for _, item := range items {
		m := item.(map[string]interface{})
		if m["__typename"] == "ConfigurableProduct" {
			found = true
			variants := m["variants"].([]interface{})
			if len(variants) == 0 {
				t.Error("configurable product has no variants")
			}
			configOpts := m["configurable_options"].([]interface{})
			if len(configOpts) == 0 {
				t.Error("configurable product has no configurable_options")
			}
			break
		}
	}
	if !found {
		t.Error("no ConfigurableProduct found in results")
	}
}

func TestBundleProduct(t *testing.T) {
	result := graphqlRequest(t, `{
		products(filter: { sku: { eq: "B5247819" } }) {
			items {
				__typename
				... on BundleProduct {
					items {
						option_id uid title required type position
						options { id uid label qty quantity is_default product { sku name } }
					}
					dynamic_price dynamic_sku dynamic_weight price_view ship_bundle_items
				}
			}
		}
	}`, "default")

	items := extractProducts(result)
	if len(items) != 1 {
		t.Fatalf("expected 1 bundle product, got %d", len(items))
	}

	item := items[0].(map[string]interface{})
	if item["__typename"] != "BundleProduct" {
		t.Fatalf("expected BundleProduct, got %v", item["__typename"])
	}

	bundleItems := item["items"].([]interface{})
	if len(bundleItems) == 0 {
		t.Fatal("expected bundle items")
	}
}

func TestPriceFilter(t *testing.T) {
	result := graphqlRequest(t, `{
		products(filter: { price: { from: "0", to: "100" } }) {
			items { sku price_range { minimum_price { final_price { value } } } }
			total_count
		}
	}`, "default")

	tc := extractField(result, "data", "products", "total_count").(float64)
	if tc < 1 {
		t.Error("expected at least 1 product in price range 0-100")
	}

	items := extractProducts(result)
	for _, item := range items {
		m := item.(map[string]interface{})
		pr := m["price_range"].(map[string]interface{})
		minP := pr["minimum_price"].(map[string]interface{})
		fp := minP["final_price"].(map[string]interface{})
		val := fp["value"].(float64)
		if val > 100 {
			t.Errorf("product %v has price %v, expected <= 100", m["sku"], val)
		}
	}
}

func TestEmptyResult(t *testing.T) {
	result := graphqlRequest(t, `{
		products(filter: { sku: { eq: "NONEXISTENT_SKU_12345" } }) {
			items { sku }
			total_count
		}
	}`, "default")

	tc := extractField(result, "data", "products", "total_count").(float64)
	if tc != 0 {
		t.Errorf("expected 0 results, got %v", tc)
	}

	items := extractProducts(result)
	if len(items) != 0 {
		t.Errorf("expected 0 items, got %d", len(items))
	}
}

func TestReviewFields(t *testing.T) {
	result := graphqlRequest(t, `{
		products(filter: { sku: { eq: "B5247819" } }) {
			items {
				sku rating_summary review_count
				reviews { items { summary nickname } page_info { total_pages } }
			}
		}
	}`, "default")

	items := extractProducts(result)
	item := items[0].(map[string]interface{})

	if _, ok := item["rating_summary"].(float64); !ok {
		t.Error("expected rating_summary to be a number")
	}

	reviews := item["reviews"].(map[string]interface{})
	if reviews["items"] == nil {
		t.Error("expected reviews.items to be non-nil")
	}
}

func TestCategoryIDFilter(t *testing.T) {
	result := graphqlRequest(t, `{
		products(filter: { sku: { eq: "B5247819" } }) {
			items { categories { uid name } }
		}
	}`, "default")

	items := extractProducts(result)
	cats := items[0].(map[string]interface{})["categories"].([]interface{})
	catUID := cats[0].(map[string]interface{})["uid"].(string)

	result2 := graphqlRequest(t, `{
		products(filter: { category_uid: { eq: "`+catUID+`" } }, pageSize: 50) {
			total_count
		}
	}`, "default")

	expectedCount := extractField(result2, "data", "products", "total_count").(float64)
	if expectedCount < 1 {
		t.Skip("no products in category")
	}

	result3 := graphqlRequest(t, `{
		products(filter: { category_id: { eq: "4" } }, pageSize: 1) {
			total_count
		}
	}`, "default")

	tc := extractField(result3, "data", "products", "total_count")
	if tc == nil {
		t.Error("expected total_count from category_id filter")
	}
}

func TestSwatchImage(t *testing.T) {
	result := graphqlRequest(t, `{
		products(pageSize: 1) {
			items { sku swatch_image }
		}
	}`, "default")

	items := extractProducts(result)
	if len(items) == 0 {
		t.Fatal("expected products")
	}
	_ = items[0].(map[string]interface{})["swatch_image"]
}

func TestOnlyXLeftInStock(t *testing.T) {
	result := graphqlRequest(t, `{
		products(pageSize: 1) {
			items { sku only_x_left_in_stock }
		}
	}`, "default")

	items := extractProducts(result)
	if len(items) == 0 {
		t.Fatal("expected products")
	}
	item := items[0].(map[string]interface{})
	if item["only_x_left_in_stock"] != nil {
		t.Log("only_x_left_in_stock is set (stock threshold configured)")
	}
}

func TestStoreMiddleware(t *testing.T) {
	result := graphqlRequest(t, `{
		products(pageSize: 1) { items { sku } }
	}`, "default")

	items := extractProducts(result)
	if len(items) == 0 {
		t.Error("expected products with store 'default'")
	}
}

func TestHealthEndpoint(t *testing.T) {
	result := graphqlRequest(t, `{ products(pageSize: 1) { total_count } }`, "default")
	tc := extractField(result, "data", "products", "total_count").(float64)
	if tc < 1 {
		t.Error("expected at least 1 product in database")
	}
}
