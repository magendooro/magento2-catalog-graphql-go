package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/magendooro/magento2-catalog-graphql-go/graph/model"
)

type URLRepository struct {
	db *sql.DB
}

func NewURLRepository(db *sql.DB) *URLRepository {
	return &URLRepository{db: db}
}

// URLRewriteData holds a URL rewrite entry.
type URLRewriteData struct {
	EntityID    int
	RequestPath string
	TargetPath  string
	StoreID     int
}

// GetURLRewritesForProducts batch-loads URL rewrites for product entity_ids.
func (r *URLRepository) GetURLRewritesForProducts(ctx context.Context, entityIDs []int, storeID int) (map[int][]*URLRewriteData, error) {
	if len(entityIDs) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(entityIDs))
	args := make([]interface{}, 0, len(entityIDs)+1)
	for i, id := range entityIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	args = append(args, storeID)

	query := fmt.Sprintf(`
		SELECT entity_id, request_path, target_path, store_id
		FROM url_rewrite
		WHERE entity_type = 'product'
		AND entity_id IN (%s)
		AND store_id = ?
	`, joinPlaceholders(placeholders))

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("url rewrite query failed: %w", err)
	}
	defer rows.Close()

	result := make(map[int][]*URLRewriteData)
	for rows.Next() {
		u := &URLRewriteData{}
		if err := rows.Scan(&u.EntityID, &u.RequestPath, &u.TargetPath, &u.StoreID); err != nil {
			return nil, fmt.Errorf("url rewrite scan failed: %w", err)
		}
		result[u.EntityID] = append(result[u.EntityID], u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("url rewrite rows iteration failed: %w", err)
	}
	return result, nil
}

// BuildURLRewrites converts URLRewriteData to GraphQL types.
func BuildURLRewrites(rewrites []*URLRewriteData) []*model.URLRewrite {
	if len(rewrites) == 0 {
		return nil
	}
	result := make([]*model.URLRewrite, 0, len(rewrites))
	for _, rw := range rewrites {
		url := rw.RequestPath
		params := parseTargetPathParams(rw.TargetPath)
		result = append(result, &model.URLRewrite{
			URL:        &url,
			Parameters: params,
		})
	}
	return result
}

// parseTargetPathParams extracts key/value parameters from Magento target_path.
// e.g., "catalog/product/view/id/876/category/3" → [{name:"id",value:"876"},{name:"category",value:"3"}]
func parseTargetPathParams(targetPath string) []*model.HTTPQueryParameter {
	parts := strings.Split(targetPath, "/")
	// Find known parameter pairs after the action path
	// Format: module/controller/action/key1/val1/key2/val2
	var params []*model.HTTPQueryParameter
	// Skip the first 3 segments (module/controller/action)
	for i := 3; i+1 < len(parts); i += 2 {
		n := parts[i]
		v := parts[i+1]
		params = append(params, &model.HTTPQueryParameter{
			Name:  &n,
			Value: &v,
		})
	}
	if len(params) == 0 {
		return nil
	}
	return params
}

// GetProductURLSuffix reads catalog/seo/product_url_suffix from core_config_data.
func (r *URLRepository) GetProductURLSuffix(ctx context.Context, storeID int) string {
	var suffix sql.NullString
	// Try store-specific first
	err := r.db.QueryRowContext(ctx,
		"SELECT value FROM core_config_data WHERE path = 'catalog/seo/product_url_suffix' AND scope_id = ? AND scope = 'stores' ORDER BY scope_id DESC LIMIT 1",
		storeID,
	).Scan(&suffix)
	if err == nil && suffix.Valid {
		return suffix.String
	}
	// Fallback to default
	err = r.db.QueryRowContext(ctx,
		"SELECT value FROM core_config_data WHERE path = 'catalog/seo/product_url_suffix' AND scope = 'default' LIMIT 1",
	).Scan(&suffix)
	if err == nil && suffix.Valid {
		return suffix.String
	}
	return ".html"
}

// GetCategoryURLSuffix reads catalog/seo/category_url_suffix from core_config_data.
func (r *URLRepository) GetCategoryURLSuffix(ctx context.Context, storeID int) string {
	var suffix sql.NullString
	err := r.db.QueryRowContext(ctx,
		"SELECT value FROM core_config_data WHERE path = 'catalog/seo/category_url_suffix' AND scope_id = ? AND scope = 'stores' ORDER BY scope_id DESC LIMIT 1",
		storeID,
	).Scan(&suffix)
	if err == nil && suffix.Valid {
		return suffix.String
	}
	err = r.db.QueryRowContext(ctx,
		"SELECT value FROM core_config_data WHERE path = 'catalog/seo/category_url_suffix' AND scope = 'default' LIMIT 1",
	).Scan(&suffix)
	if err == nil && suffix.Valid {
		return suffix.String
	}
	return ".html"
}

// GetWebsiteIDForStore returns the website_id for a given store_id.
func (r *URLRepository) GetWebsiteIDForStore(ctx context.Context, storeID int) int {
	var websiteID int
	err := r.db.QueryRowContext(ctx, "SELECT website_id FROM store WHERE store_id = ?", storeID).Scan(&websiteID)
	if err != nil {
		return 1 // default
	}
	return websiteID
}

// GetBaseCurrency returns the base currency code.
func (r *URLRepository) GetBaseCurrency(ctx context.Context) string {
	var currency string
	err := r.db.QueryRowContext(ctx,
		"SELECT value FROM core_config_data WHERE path = 'currency/options/base' AND scope = 'default' LIMIT 1",
	).Scan(&currency)
	if err != nil {
		return "USD"
	}
	return currency
}
