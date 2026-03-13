package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// Comparison tests: sends the same GraphQL query to both the Go service and
// Magento, then diffs the responses field by field.
//
// Prerequisites:
//   - Go service running at GO_GRAPHQL_URL   (default http://localhost:8080/graphql)
//   - Magento  running at MAGE_GRAPHQL_URL   (default http://localhost/graphql)
//
// Run:
//   go test -v -run TestCompare -count=1 -timeout 120s

const (
	defaultGoURL   = "http://localhost:8080/graphql"
	defaultMageURL = "http://localhost/graphql"
	storeCode      = "default"
)

func goURL() string {
	if u := os.Getenv("GO_GRAPHQL_URL"); u != "" {
		return u
	}
	return defaultGoURL
}

func mageURL() string {
	if u := os.Getenv("MAGE_GRAPHQL_URL"); u != "" {
		return u
	}
	return defaultMageURL
}

// ---------- helpers ----------

type queryResult struct {
	Data   map[string]interface{} `json:"data"`
	Errors []interface{}          `json:"errors"`
}

func queryEndpoint(url, query, store string) (*queryResult, time.Duration, error) {
	body, _ := json.Marshal(map[string]string{"query": query})
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if store != "" {
		req.Header.Set("Store", store)
	}

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	elapsed := time.Since(start)
	if err != nil {
		return nil, elapsed, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, elapsed, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(raw))
	}

	var result queryResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, elapsed, fmt.Errorf("JSON decode: %w", err)
	}
	return &result, elapsed, nil
}

func queryBoth(t *testing.T, query string) (goRes, mageRes *queryResult) {
	t.Helper()

	var goErr, mageErr error
	var goTime, mageTime time.Duration

	goRes, goTime, goErr = queryEndpoint(goURL(), query, storeCode)
	if goErr != nil {
		t.Fatalf("Go service error: %v", goErr)
	}

	mageRes, mageTime, mageErr = queryEndpoint(mageURL(), query, storeCode)
	if mageErr != nil {
		t.Fatalf("Magento error: %v", mageErr)
	}

	t.Logf("Timing: Go=%v  Magento=%v  (%.1fx faster)", goTime, mageTime, float64(mageTime)/float64(goTime))

	if len(goRes.Errors) > 0 {
		t.Logf("Go errors: %v", goRes.Errors)
	}
	if len(mageRes.Errors) > 0 {
		t.Logf("Magento errors: %v", mageRes.Errors)
	}

	return goRes, mageRes
}

// ---------- diff engine ----------

type diff struct {
	Path     string
	Go       interface{}
	Magento  interface{}
	Severity string // "MISMATCH", "MISSING_GO", "MISSING_MAGE", "ORDER"
}

func (d diff) String() string {
	return fmt.Sprintf("[%s] %s\n  Go:      %v\n  Magento: %v", d.Severity, d.Path, d.Go, d.Magento)
}

// deepDiff compares two JSON-decoded values and returns differences.
// ignorePaths contains path prefixes to skip (e.g., "items.0.uid" if UIDs differ by design).
func deepDiff(path string, a, b interface{}, ignorePaths map[string]bool) []diff {
	if ignorePaths[path] {
		return nil
	}

	var diffs []diff

	switch av := a.(type) {
	case map[string]interface{}:
		bv, ok := b.(map[string]interface{})
		if !ok {
			return []diff{{Path: path, Go: typeStr(a), Magento: typeStr(b), Severity: "MISMATCH"}}
		}
		allKeys := map[string]bool{}
		for k := range av {
			allKeys[k] = true
		}
		for k := range bv {
			allKeys[k] = true
		}
		for k := range allKeys {
			childPath := path + "." + k
			if ignorePaths[childPath] {
				continue
			}
			_, inA := av[k]
			_, inB := bv[k]
			if inA && !inB {
				diffs = append(diffs, diff{Path: childPath, Go: av[k], Magento: "(absent)", Severity: "MISSING_MAGE"})
			} else if !inA && inB {
				diffs = append(diffs, diff{Path: childPath, Go: "(absent)", Magento: bv[k], Severity: "MISSING_GO"})
			} else {
				diffs = append(diffs, deepDiff(childPath, av[k], bv[k], ignorePaths)...)
			}
		}

	case []interface{}:
		bv, ok := b.([]interface{})
		if !ok {
			return []diff{{Path: path, Go: typeStr(a), Magento: typeStr(b), Severity: "MISMATCH"}}
		}
		if len(av) != len(bv) {
			diffs = append(diffs, diff{Path: path + ".length", Go: len(av), Magento: len(bv), Severity: "MISMATCH"})
		}
		minLen := len(av)
		if len(bv) < minLen {
			minLen = len(bv)
		}
		for i := 0; i < minLen; i++ {
			childPath := fmt.Sprintf("%s.%d", path, i)
			diffs = append(diffs, deepDiff(childPath, av[i], bv[i], ignorePaths)...)
		}

	case float64:
		bv, ok := b.(float64)
		if !ok {
			return []diff{{Path: path, Go: a, Magento: b, Severity: "MISMATCH"}}
		}
		if math.Abs(av-bv) > 0.01 {
			return []diff{{Path: path, Go: av, Magento: bv, Severity: "MISMATCH"}}
		}

	case string:
		bv, ok := b.(string)
		if !ok {
			return []diff{{Path: path, Go: a, Magento: b, Severity: "MISMATCH"}}
		}
		if av != bv {
			return []diff{{Path: path, Go: av, Magento: bv, Severity: "MISMATCH"}}
		}

	case bool:
		bv, ok := b.(bool)
		if !ok || av != bv {
			return []diff{{Path: path, Go: a, Magento: b, Severity: "MISMATCH"}}
		}

	case nil:
		if b != nil {
			return []diff{{Path: path, Go: nil, Magento: b, Severity: "MISMATCH"}}
		}

	default:
		if !reflect.DeepEqual(a, b) {
			return []diff{{Path: path, Go: a, Magento: b, Severity: "MISMATCH"}}
		}
	}

	return diffs
}

func typeStr(v interface{}) string {
	if v == nil {
		return "null"
	}
	return fmt.Sprintf("%T", v)
}

// sortItemsBySKU sorts product items array by SKU for stable comparison.
func sortItemsBySKU(items []interface{}) {
	sort.Slice(items, func(i, j int) bool {
		mi, _ := items[i].(map[string]interface{})
		mj, _ := items[j].(map[string]interface{})
		si, _ := mi["sku"].(string)
		sj, _ := mj["sku"].(string)
		return si < sj
	})
}

// sortArrayByField sorts a slice of maps by a given string field.
func sortArrayByField(arr []interface{}, field string) {
	sort.Slice(arr, func(i, j int) bool {
		mi, _ := arr[i].(map[string]interface{})
		mj, _ := arr[j].(map[string]interface{})
		si, _ := mi[field].(string)
		sj, _ := mj[field].(string)
		return si < sj
	})
}

func getProducts(res *queryResult) map[string]interface{} {
	if res.Data == nil {
		return map[string]interface{}{}
	}
	p, ok := res.Data["products"].(map[string]interface{})
	if !ok {
		return map[string]interface{}{}
	}
	return p
}

func getItems(res *queryResult) []interface{} {
	p := getProducts(res)
	items, _ := p["items"].([]interface{})
	return items
}

func reportDiffs(t *testing.T, diffs []diff) {
	t.Helper()
	if len(diffs) == 0 {
		t.Log("IDENTICAL")
		return
	}
	for _, d := range diffs {
		t.Errorf("%s", d)
	}
	t.Logf("Total differences: %d", len(diffs))
}

// ---------- comparison tests ----------

func TestCompareBasicSKULookup(t *testing.T) {
	query := `{
		products(filter: { sku: { eq: "B5247819" } }) {
			items {
				sku name __typename type_id
				uid attribute_set_id
				created_at updated_at
			}
			total_count
		}
	}`

	goRes, mageRes := queryBoth(t, query)

	ignore := map[string]bool{}
	diffs := deepDiff("products", getProducts(goRes), getProducts(mageRes), ignore)
	reportDiffs(t, diffs)
}

func TestCompareMetaFields(t *testing.T) {
	query := `{
		products(filter: { sku: { eq: "B5247819" } }) {
			items {
				sku
				meta_title meta_keyword meta_description
				description { html } short_description { html }
				options_container
				manufacturer
				country_of_manufacture
				gift_message_available
			}
		}
	}`

	goRes, mageRes := queryBoth(t, query)
	diffs := deepDiff("products", getProducts(goRes), getProducts(mageRes), map[string]bool{})
	reportDiffs(t, diffs)
}

func TestComparePriceRange(t *testing.T) {
	query := `{
		products(filter: { sku: { eq: "A1358093" } }) {
			items {
				sku
				special_price special_from_date special_to_date
				price_range {
					minimum_price {
						regular_price { value currency }
						final_price { value currency }
						discount { amount_off percent_off }
					}
					maximum_price {
						regular_price { value currency }
						final_price { value currency }
						discount { amount_off percent_off }
					}
				}
			}
		}
	}`

	goRes, mageRes := queryBoth(t, query)
	diffs := deepDiff("products", getProducts(goRes), getProducts(mageRes), map[string]bool{})
	reportDiffs(t, diffs)
}

func TestComparePriceTiers(t *testing.T) {
	query := `{
		products(filter: { sku: { eq: "A1358093" } }) {
			items {
				sku
				price_tiers {
					quantity
					final_price { value currency }
					discount { amount_off percent_off }
				}
			}
		}
	}`

	goRes, mageRes := queryBoth(t, query)
	diffs := deepDiff("products", getProducts(goRes), getProducts(mageRes), map[string]bool{})
	reportDiffs(t, diffs)
}

func TestCompareImages(t *testing.T) {
	query := `{
		products(filter: { sku: { eq: "A1358093" } }) {
			items {
				sku
				image { url label disabled }
				small_image { url label disabled }
				thumbnail { url label disabled }
				swatch_image
			}
		}
	}`

	goRes, mageRes := queryBoth(t, query)
	diffs := deepDiff("products", getProducts(goRes), getProducts(mageRes), map[string]bool{})
	reportDiffs(t, diffs)
}

func TestCompareMediaGallery(t *testing.T) {
	query := `{
		products(filter: { sku: { eq: "A1358093" } }) {
			items {
				sku
				media_gallery {
					url label position disabled
				}
			}
		}
	}`

	goRes, mageRes := queryBoth(t, query)

	// Sort media gallery by position for stable comparison
	for _, res := range []*queryResult{goRes, mageRes} {
		items := getItems(res)
		for _, item := range items {
			m := item.(map[string]interface{})
			if gallery, ok := m["media_gallery"].([]interface{}); ok {
				sortArrayByField(gallery, "url")
			}
		}
	}

	diffs := deepDiff("products", getProducts(goRes), getProducts(mageRes), map[string]bool{})
	reportDiffs(t, diffs)
}

func TestCompareInventory(t *testing.T) {
	query := `{
		products(filter: { sku: { eq: "A1358093" } }) {
			items {
				sku
				stock_status
				only_x_left_in_stock
			}
		}
	}`

	goRes, mageRes := queryBoth(t, query)
	diffs := deepDiff("products", getProducts(goRes), getProducts(mageRes), map[string]bool{})
	reportDiffs(t, diffs)
}

func TestCompareCategories(t *testing.T) {
	query := `{
		products(filter: { sku: { eq: "A1358093" } }) {
			items {
				sku
				categories {
					uid name url_key url_path level
					position
				}
			}
		}
	}`

	goRes, mageRes := queryBoth(t, query)

	// Sort categories by uid for stable comparison
	for _, res := range []*queryResult{goRes, mageRes} {
		items := getItems(res)
		for _, item := range items {
			m := item.(map[string]interface{})
			if cats, ok := m["categories"].([]interface{}); ok {
				sortArrayByField(cats, "uid")
			}
		}
	}

	diffs := deepDiff("products", getProducts(goRes), getProducts(mageRes), map[string]bool{})
	reportDiffs(t, diffs)
}

func TestCompareURLFields(t *testing.T) {
	query := `{
		products(filter: { sku: { eq: "A1358093" } }) {
			items {
				sku
				url_key url_suffix canonical_url
				url_rewrites { url parameters { name value } }
			}
		}
	}`

	goRes, mageRes := queryBoth(t, query)

	// Sort url_rewrites by url for stable comparison
	for _, res := range []*queryResult{goRes, mageRes} {
		items := getItems(res)
		for _, item := range items {
			m := item.(map[string]interface{})
			if rewrites, ok := m["url_rewrites"].([]interface{}); ok {
				sortArrayByField(rewrites, "url")
			}
		}
	}

	diffs := deepDiff("products", getProducts(goRes), getProducts(mageRes), map[string]bool{})
	reportDiffs(t, diffs)
}

func TestComparePagination(t *testing.T) {
	query := `{
		products(filter: { sku: { in: ["A1358093", "A1358094", "A1358095"] } }, pageSize: 2, currentPage: 1, sort: { name: ASC }) {
			items { sku name }
			total_count
			page_info { page_size current_page total_pages }
		}
	}`

	goRes, mageRes := queryBoth(t, query)
	diffs := deepDiff("products", getProducts(goRes), getProducts(mageRes), map[string]bool{})
	reportDiffs(t, diffs)
}

func TestCompareSorting(t *testing.T) {
	query := `{
		products(filter: { sku: { in: ["A1358093", "A1358094", "A1358095", "A1358096", "A1358097"] } }, sort: { name: ASC }) {
			items { sku name }
		}
	}`

	goRes, mageRes := queryBoth(t, query)
	// Don't sort — order IS the thing being tested
	diffs := deepDiff("products", getProducts(goRes), getProducts(mageRes), map[string]bool{})
	reportDiffs(t, diffs)
}

func TestComparePriceSortDESC(t *testing.T) {
	query := `{
		products(filter: { sku: { in: ["A1358093", "A1358094", "A1358095", "A1358096", "A1358097"] } }, sort: { price: DESC }) {
			items { sku name price_range { minimum_price { final_price { value } } } }
		}
	}`

	goRes, mageRes := queryBoth(t, query)
	diffs := deepDiff("products", getProducts(goRes), getProducts(mageRes), map[string]bool{})
	reportDiffs(t, diffs)
}

func TestCompareCategoryFilter(t *testing.T) {
	query := `{
		products(filter: { category_uid: { eq: "Mw==" } }, pageSize: 50, sort: { name: ASC }) {
			items { sku name __typename }
			total_count
		}
	}`

	goRes, mageRes := queryBoth(t, query)
	diffs := deepDiff("products", getProducts(goRes), getProducts(mageRes), map[string]bool{})
	reportDiffs(t, diffs)
}

func TestComparePriceFilter(t *testing.T) {
	query := `{
		products(filter: { price: { from: "50", to: "200" } }, pageSize: 100, sort: { price: ASC }) {
			items { sku name price_range { minimum_price { final_price { value currency } } } }
			total_count
		}
	}`

	goRes, mageRes := queryBoth(t, query)

	// Sort by SKU: MySQL and Elasticsearch may order same-price products differently
	for _, res := range []*queryResult{goRes, mageRes} {
		items := getItems(res)
		sortItemsBySKU(items)
	}

	diffs := deepDiff("products", getProducts(goRes), getProducts(mageRes), map[string]bool{})
	reportDiffs(t, diffs)
}

func TestCompareNameFilter(t *testing.T) {
	query := `{
		products(filter: { name: { match: "Aura" } }, sort: { name: ASC }, pageSize: 100) {
			items { sku name }
			total_count
		}
	}`

	goRes, mageRes := queryBoth(t, query)

	// Sort items by SKU for stable comparison (MySQL vs Elasticsearch collation differs on edge cases)
	for _, res := range []*queryResult{goRes, mageRes} {
		items := getItems(res)
		sort.Slice(items, func(i, j int) bool {
			si := items[i].(map[string]interface{})["sku"].(string)
			sj := items[j].(map[string]interface{})["sku"].(string)
			return si < sj
		})
	}

	diffs := deepDiff("products", getProducts(goRes), getProducts(mageRes), map[string]bool{})
	reportDiffs(t, diffs)
}

func TestCompareConfigurableProduct(t *testing.T) {
	// Find a configurable product SKU first
	query := `{
		products(filter: { sku: { in: ["A1358093"] } }) {
			items {
				sku name __typename
				... on ConfigurableProduct {
					configurable_options {
						attribute_code label position
						values { value_index label uid swatch_data { value } }
					}
					variants {
						attributes { label code value_index uid }
						product {
							sku name
							stock_status
							price_range {
								minimum_price { final_price { value currency } }
							}
						}
					}
				}
			}
		}
	}`

	goRes, mageRes := queryBoth(t, query)

	// Sort variants by child SKU, configurable_options by attribute_code
	for _, res := range []*queryResult{goRes, mageRes} {
		items := getItems(res)
		for _, item := range items {
			m := item.(map[string]interface{})
			if variants, ok := m["variants"].([]interface{}); ok {
				sort.Slice(variants, func(i, j int) bool {
					pi := variants[i].(map[string]interface{})["product"].(map[string]interface{})
					pj := variants[j].(map[string]interface{})["product"].(map[string]interface{})
					return pi["sku"].(string) < pj["sku"].(string)
				})
			}
			if opts, ok := m["configurable_options"].([]interface{}); ok {
				sortArrayByField(opts, "attribute_code")
				for _, opt := range opts {
					om := opt.(map[string]interface{})
					if vals, ok := om["values"].([]interface{}); ok {
						sortArrayByField(vals, "label")
					}
				}
			}
		}
	}

	diffs := deepDiff("products", getProducts(goRes), getProducts(mageRes), map[string]bool{})
	reportDiffs(t, diffs)
}

func TestCompareBundleProduct(t *testing.T) {
	query := `{
		products(filter: { sku: { eq: "B5247819" } }) {
			items {
				sku name __typename
				... on BundleProduct {
					dynamic_price dynamic_sku dynamic_weight
					price_view ship_bundle_items
					items {
						option_id uid title required type position
						options {
							id uid label qty quantity is_default
							product { sku name }
						}
					}
				}
			}
		}
	}`

	goRes, mageRes := queryBoth(t, query)

	// Sort bundle items by option_id, options within by id
	for _, res := range []*queryResult{goRes, mageRes} {
		for _, item := range getItems(res) {
			m := item.(map[string]interface{})
			if bundleItems, ok := m["items"].([]interface{}); ok {
				sort.Slice(bundleItems, func(i, j int) bool {
					oi := bundleItems[i].(map[string]interface{})["option_id"].(float64)
					oj := bundleItems[j].(map[string]interface{})["option_id"].(float64)
					return oi < oj
				})
				for _, bi := range bundleItems {
					bm := bi.(map[string]interface{})
					if opts, ok := bm["options"].([]interface{}); ok {
						sort.Slice(opts, func(i, j int) bool {
							oi := opts[i].(map[string]interface{})["id"].(float64)
							oj := opts[j].(map[string]interface{})["id"].(float64)
							return oi < oj
						})
					}
				}
			}
		}
	}

	diffs := deepDiff("products", getProducts(goRes), getProducts(mageRes), map[string]bool{})
	reportDiffs(t, diffs)
}

func TestCompareRelatedProducts(t *testing.T) {
	query := `{
		products(filter: { sku: { eq: "A1358093" } }) {
			items {
				sku
				related_products { sku name __typename }
				upsell_products { sku name __typename }
				crosssell_products { sku name __typename }
			}
		}
	}`

	goRes, mageRes := queryBoth(t, query)

	// Sort related product arrays by SKU
	for _, res := range []*queryResult{goRes, mageRes} {
		items := getItems(res)
		for _, item := range items {
			m := item.(map[string]interface{})
			for _, field := range []string{"related_products", "upsell_products", "crosssell_products"} {
				if arr, ok := m[field].([]interface{}); ok {
					sortArrayByField(arr, "sku")
				}
			}
		}
	}

	diffs := deepDiff("products", getProducts(goRes), getProducts(mageRes), map[string]bool{})
	reportDiffs(t, diffs)
}

func TestCompareReviews(t *testing.T) {
	query := `{
		products(filter: { sku: { eq: "A1358093" } }) {
			items {
				sku
				rating_summary review_count
				reviews(pageSize: 5) {
					items {
						summary text nickname created_at
						average_rating
						ratings_breakdown { name value }
					}
					page_info { page_size current_page total_pages }
				}
			}
		}
	}`

	goRes, mageRes := queryBoth(t, query)
	diffs := deepDiff("products", getProducts(goRes), getProducts(mageRes), map[string]bool{})
	reportDiffs(t, diffs)
}

func TestCompareAggregations(t *testing.T) {
	query := `{
		products(filter: { category_uid: { eq: "Mw==" } }) {
			aggregations {
				attribute_code label count
				options { label value count }
			}
		}
	}`

	goRes, mageRes := queryBoth(t, query)

	// Sort aggregations by attribute_code, options by value
	for _, res := range []*queryResult{goRes, mageRes} {
		p := getProducts(res)
		if aggs, ok := p["aggregations"].([]interface{}); ok {
			sortArrayByField(aggs, "attribute_code")
			for _, agg := range aggs {
				am := agg.(map[string]interface{})
				if opts, ok := am["options"].([]interface{}); ok {
					sortArrayByField(opts, "value")
				}
			}
		}
	}

	diffs := deepDiff("products", getProducts(goRes), getProducts(mageRes), map[string]bool{})
	reportDiffs(t, diffs)
}

func TestCompareSortFields(t *testing.T) {
	query := `{
		products(filter: { sku: { eq: "A1358093" } }) {
			sort_fields {
				default
				options { value label }
			}
		}
	}`

	goRes, mageRes := queryBoth(t, query)

	// Sort options by value
	for _, res := range []*queryResult{goRes, mageRes} {
		p := getProducts(res)
		if sf, ok := p["sort_fields"].(map[string]interface{}); ok {
			if opts, ok := sf["options"].([]interface{}); ok {
				sortArrayByField(opts, "value")
			}
		}
	}

	diffs := deepDiff("products", getProducts(goRes), getProducts(mageRes), map[string]bool{})
	reportDiffs(t, diffs)
}

func TestCompareMultiSKU(t *testing.T) {
	query := `{
		products(filter: { sku: { in: ["A1358093", "A1358094", "B5247819"] } }, sort: { name: ASC }) {
			items {
				sku name __typename type_id
				price_range {
					minimum_price {
						regular_price { value currency }
						final_price { value currency }
					}
				}
				stock_status
				categories { uid name }
			}
			total_count
		}
	}`

	goRes, mageRes := queryBoth(t, query)

	// Sort categories within each item
	for _, res := range []*queryResult{goRes, mageRes} {
		items := getItems(res)
		for _, item := range items {
			m := item.(map[string]interface{})
			if cats, ok := m["categories"].([]interface{}); ok {
				sortArrayByField(cats, "uid")
			}
		}
	}

	diffs := deepDiff("products", getProducts(goRes), getProducts(mageRes), map[string]bool{})
	reportDiffs(t, diffs)
}

func TestCompareURLKeyFilter(t *testing.T) {
	query := `{
		products(filter: { url_key: { eq: "bundle-aura" } }) {
			items { sku name url_key }
			total_count
		}
	}`

	goRes, mageRes := queryBoth(t, query)
	diffs := deepDiff("products", getProducts(goRes), getProducts(mageRes), map[string]bool{})
	reportDiffs(t, diffs)
}

func TestCompareNewDates(t *testing.T) {
	query := `{
		products(filter: { sku: { eq: "A1358093" } }) {
			items {
				sku
				new_from_date new_to_date
				special_from_date special_to_date
			}
		}
	}`

	goRes, mageRes := queryBoth(t, query)
	diffs := deepDiff("products", getProducts(goRes), getProducts(mageRes), map[string]bool{})
	reportDiffs(t, diffs)
}

func TestCompareWeight(t *testing.T) {
	query := `{
		products(filter: { sku: { eq: "A1358093" } }) {
			items {
				sku __typename
				... on SimpleProduct { weight }
			}
		}
	}`

	goRes, mageRes := queryBoth(t, query)
	diffs := deepDiff("products", getProducts(goRes), getProducts(mageRes), map[string]bool{})
	reportDiffs(t, diffs)
}

func TestCompareEmptyResult(t *testing.T) {
	query := `{
		products(filter: { sku: { eq: "NONEXISTENT_SKU_99999" } }) {
			items { sku }
			total_count
			page_info { page_size current_page total_pages }
		}
	}`

	goRes, mageRes := queryBoth(t, query)
	diffs := deepDiff("products", getProducts(goRes), getProducts(mageRes), map[string]bool{})
	reportDiffs(t, diffs)
}

// ---------- summary ----------

func TestCompareSummary(t *testing.T) {
	// This test runs a broad query to compare many fields at once
	query := `{
		products(filter: { sku: { eq: "A1358093" } }) {
			items {
				uid sku name __typename type_id
				attribute_set_id
				description { html } short_description { html }
				meta_title meta_keyword meta_description
				special_price special_from_date special_to_date
				new_from_date new_to_date
				options_container manufacturer country_of_manufacture
				gift_message_available
				image { url label disabled }
				small_image { url label disabled }
				thumbnail { url label disabled }
				swatch_image
				price_range {
					minimum_price {
						regular_price { value currency }
						final_price { value currency }
						discount { amount_off percent_off }
					}
					maximum_price {
						regular_price { value currency }
						final_price { value currency }
						discount { amount_off percent_off }
					}
				}
				price_tiers {
					quantity
					final_price { value currency }
					discount { amount_off percent_off }
				}
				stock_status only_x_left_in_stock
				url_key url_suffix canonical_url
				rating_summary review_count
				categories { uid name url_key level position }
			}
			total_count
		}
	}`

	goRes, mageRes := queryBoth(t, query)

	// Sort categories by uid
	for _, res := range []*queryResult{goRes, mageRes} {
		items := getItems(res)
		for _, item := range items {
			m := item.(map[string]interface{})
			if cats, ok := m["categories"].([]interface{}); ok {
				sortArrayByField(cats, "uid")
			}
		}
	}

	diffs := deepDiff("products", getProducts(goRes), getProducts(mageRes), map[string]bool{})

	if len(diffs) == 0 {
		t.Log("ALL FIELDS MATCH - Go service is a faithful reproduction of Magento's output")
	} else {
		// Group diffs by severity
		byType := map[string][]diff{}
		for _, d := range diffs {
			byType[d.Severity] = append(byType[d.Severity], d)
		}
		for sev, ds := range byType {
			t.Logf("\n--- %s (%d) ---", sev, len(ds))
			for _, d := range ds {
				t.Logf("  %s", d)
			}
		}

		mismatches := len(byType["MISMATCH"])
		if mismatches > 0 {
			t.Errorf("%d field value mismatches found", mismatches)
		}

		fields := 0
		goItems := getItems(goRes)
		if len(goItems) > 0 {
			fields = countFields(goItems[0])
		}
		t.Logf("\nComparison: %d fields checked, %d differences found", fields, len(diffs))
	}
}

func countFields(v interface{}) int {
	switch val := v.(type) {
	case map[string]interface{}:
		count := 0
		for _, child := range val {
			count += countFields(child)
		}
		return count + len(val)
	case []interface{}:
		if len(val) > 0 {
			return countFields(val[0])
		}
		return 0
	default:
		return 0
	}
}

// ---------- performance comparison ----------

func TestComparePerformance(t *testing.T) {
	queries := []struct {
		name  string
		query string
	}{
		{"SKU lookup", `{ products(filter: { sku: { eq: "B5247819" } }) { items { sku name } total_count } }`},
		{"Category filter", `{ products(filter: { category_uid: { eq: "Mw==" } }, pageSize: 50) { items { sku name } total_count } }`},
		{"Full product", `{ products(filter: { sku: { eq: "A1358093" } }) { items { sku name price_range { minimum_price { final_price { value currency } } } stock_status categories { name } media_gallery { url } url_key } } }`},
		{"Multi-SKU sorted", `{ products(filter: { sku: { in: ["A1358093","A1358094","A1358095","A1358096","A1358097"] } }, sort: { name: ASC }) { items { sku name } total_count } }`},
	}

	t.Logf("\n%-25s %12s %12s %10s", "Query", "Go", "Magento", "Speedup")
	t.Logf("%s", strings.Repeat("-", 62))

	for _, q := range queries {
		var goTotal, mageTotal time.Duration
		runs := 3

		for i := 0; i < runs; i++ {
			_, goTime, _ := queryEndpoint(goURL(), q.query, storeCode)
			_, mageTime, _ := queryEndpoint(mageURL(), q.query, storeCode)
			goTotal += goTime
			mageTotal += mageTime
		}

		goAvg := goTotal / time.Duration(runs)
		mageAvg := mageTotal / time.Duration(runs)
		speedup := float64(mageAvg) / float64(goAvg)

		t.Logf("%-25s %12v %12v %9.1fx", q.name, goAvg.Round(time.Millisecond), mageAvg.Round(time.Millisecond), speedup)
	}
}
