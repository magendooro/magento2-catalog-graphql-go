package service

import (
	"context"

	"github.com/99designs/gqlgen/graphql"
)

// RequestedFields tracks which product sub-fields were requested in the GraphQL query.
// This allows skipping batch loads for data the client doesn't need.
type RequestedFields struct {
	PriceRange    bool
	PriceTiers    bool
	MediaGallery  bool
	Inventory     bool // stock_status, quantity, min_sale_qty, max_sale_qty
	Categories    bool
	URLRewrites   bool
	Related       bool // related_products, upsell_products, crosssell_products
	Reviews       bool // rating_summary, review_count, reviews
	Configurable  bool // variants, configurable_options
	Bundle        bool // bundle items
	Aggregations  bool
	SortFields    bool
	Suggestions   bool
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
			// Inspect item-level fields
			for _, itemField := range graphql.CollectFields(graphql.GetOperationContext(ctx), pf.Selections, nil) {
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
				case "rating_summary", "review_count", "reviews":
					rf.Reviews = true
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
