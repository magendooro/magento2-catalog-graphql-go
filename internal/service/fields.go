package service

import (
	"context"
	"strconv"

	"github.com/99designs/gqlgen/graphql"
	"github.com/vektah/gqlparser/v2/ast"
)

// RequestedFields tracks which product sub-fields were requested in the GraphQL query.
// This allows skipping batch loads for data the client doesn't need.
type RequestedFields struct {
	PriceRange      bool
	PriceTiers      bool
	MediaGallery    bool
	Inventory       bool // stock_status, quantity, min_sale_qty, max_sale_qty
	Categories      bool
	URLRewrites     bool
	Related         bool // related_products, upsell_products, crosssell_products
	Reviews         bool // rating_summary, review_count, reviews
	Configurable    bool // variants, configurable_options
	Bundle          bool // bundle items
	Aggregations    bool
	SortFields      bool
	Suggestions     bool
	ReviewPageSize  int // from reviews(pageSize:) argument — 0 means use schema default (20)
	ReviewCurrentPage int // from reviews(currentPage:) argument — 0 means use schema default (1)
}

// CollectRequestedFields inspects the GraphQL operation context to determine
// which product sub-fields are requested, enabling selective batch loading.
func CollectRequestedFields(ctx context.Context) *RequestedFields {
	rf := &RequestedFields{}

	// Collect top-level Products fields
	productsFields := graphql.CollectFieldsCtx(ctx, nil)

	for _, pf := range productsFields {
		switch pf.Name {
		case "aggregations":
			rf.Aggregations = true
		case "sort_fields":
			rf.SortFields = true
		case "suggestions":
			rf.Suggestions = true
		case "items":
			// Inspect item-level fields — include all product type fragments so
			// ... on ConfigurableProduct { configurable_options } is collected
			productTypes := []string{"SimpleProduct", "ConfigurableProduct", "BundleProduct", "VirtualProduct", "GroupedProduct"}
			for _, itemField := range graphql.CollectFields(graphql.GetOperationContext(ctx), pf.Selections, productTypes) {
				switch itemField.Name {
				case "price_range":
					rf.PriceRange = true
				case "price_tiers":
					rf.PriceTiers = true
				case "media_gallery":
					rf.MediaGallery = true
				case "stock_status", "quantity", "min_sale_qty", "max_sale_qty", "only_x_left_in_stock":
					rf.Inventory = true
				case "categories":
					rf.Categories = true
				case "url_rewrites":
					rf.URLRewrites = true
				case "related_products", "upsell_products", "crosssell_products":
					rf.Related = true
				case "rating_summary", "review_count":
					rf.Reviews = true
				case "reviews":
					rf.Reviews = true
					rf.ReviewPageSize, rf.ReviewCurrentPage = extractReviewArgs(ctx, itemField)
				case "variants", "configurable_options":
					rf.Configurable = true
				case "items", "dynamic_price", "dynamic_sku", "dynamic_weight", "price_view", "ship_bundle_items":
					rf.Bundle = true
				case "image", "small_image", "thumbnail":
					// These come from EAV, not separate queries — no extra load needed
				}
			}
		}
	}

	return rf
}

// extractReviewArgs reads pageSize and currentPage from a reviews field's arguments.
// Returns (pageSize, currentPage) with 0 meaning "use schema default".
func extractReviewArgs(ctx context.Context, field graphql.CollectedField) (pageSize, currentPage int) {
	vars := graphql.GetOperationContext(ctx).Variables

	if arg := field.Arguments.ForName("pageSize"); arg != nil {
		pageSize = resolveIntArg(arg.Value, vars)
	}
	if arg := field.Arguments.ForName("currentPage"); arg != nil {
		currentPage = resolveIntArg(arg.Value, vars)
	}
	return pageSize, currentPage
}

// resolveIntArg evaluates a GraphQL argument value to an int.
// Handles both literal integers and variable references.
func resolveIntArg(val *ast.Value, vars map[string]interface{}) int {
	if val == nil {
		return 0
	}
	switch val.Kind {
	case ast.IntValue:
		if n, err := strconv.Atoi(val.Raw); err == nil {
			return n
		}
	case ast.Variable:
		if vars != nil {
			if v, ok := vars[val.Raw]; ok {
				switch n := v.(type) {
				case int:
					return n
				case int64:
					return int(n)
				case float64:
					return int(n)
				case string:
					if i, err := strconv.Atoi(n); err == nil {
						return i
					}
				}
			}
		}
	}
	return 0
}
