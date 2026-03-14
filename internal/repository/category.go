package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/magendooro/magento2-catalog-graphql-go/graph/model"
)

type CategoryRepository struct {
	db *sql.DB
}

func NewCategoryRepository(db *sql.DB) *CategoryRepository {
	return &CategoryRepository{db: db}
}

// CategoryData holds category data with resolved EAV attributes.
type CategoryData struct {
	EntityID    int
	ParentID    int
	Path        string
	Position    int
	Level       int
	Name        *string
	URLKey      *string
	URLPath     *string
	Description *string
	IsActive    *int
}

// GetCategoriesForProducts batch-loads categories for a list of product entity_ids.
// Returns map keyed by product entity_id.
func (r *CategoryRepository) GetCategoriesForProducts(ctx context.Context, entityIDs []int, storeID int) (map[int][]*CategoryData, error) {
	if len(entityIDs) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(entityIDs))
	args := make([]interface{}, len(entityIDs))
	for i, id := range entityIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	// Join catalog_category_product → catalog_category_entity → EAV attributes
	// Category EAV also uses row_id in Magento EE
	query := fmt.Sprintf(`
		SELECT
			ccp.product_id,
			cce.entity_id,
			cce.parent_id,
			cce.path,
			cce.position,
			cce.level,
			COALESCE(name_s.value, name_d.value) AS name,
			COALESCE(urlkey_s.value, urlkey_d.value) AS url_key,
			COALESCE(urlpath_s.value, urlpath_d.value) AS url_path,
			COALESCE(desc_s.value, desc_d.value) AS description,
			COALESCE(active_s.value, active_d.value) AS is_active
		FROM catalog_category_product ccp
		INNER JOIN catalog_category_entity cce ON ccp.category_id = cce.entity_id
		LEFT JOIN catalog_category_entity_varchar name_d ON cce.row_id = name_d.row_id AND name_d.attribute_id = 45 AND name_d.store_id = 0
		LEFT JOIN catalog_category_entity_varchar name_s ON cce.row_id = name_s.row_id AND name_s.attribute_id = 45 AND name_s.store_id = %d
		LEFT JOIN catalog_category_entity_varchar urlkey_d ON cce.row_id = urlkey_d.row_id AND urlkey_d.attribute_id = 124 AND urlkey_d.store_id = 0
		LEFT JOIN catalog_category_entity_varchar urlkey_s ON cce.row_id = urlkey_s.row_id AND urlkey_s.attribute_id = 124 AND urlkey_s.store_id = %d
		LEFT JOIN catalog_category_entity_varchar urlpath_d ON cce.row_id = urlpath_d.row_id AND urlpath_d.attribute_id = 125 AND urlpath_d.store_id = 0
		LEFT JOIN catalog_category_entity_varchar urlpath_s ON cce.row_id = urlpath_s.row_id AND urlpath_s.attribute_id = 125 AND urlpath_s.store_id = %d
		LEFT JOIN catalog_category_entity_text desc_d ON cce.row_id = desc_d.row_id AND desc_d.attribute_id = 47 AND desc_d.store_id = 0
		LEFT JOIN catalog_category_entity_text desc_s ON cce.row_id = desc_s.row_id AND desc_s.attribute_id = 47 AND desc_s.store_id = %d
		LEFT JOIN catalog_category_entity_int active_d ON cce.row_id = active_d.row_id AND active_d.attribute_id = 46 AND active_d.store_id = 0
		LEFT JOIN catalog_category_entity_int active_s ON cce.row_id = active_s.row_id AND active_s.attribute_id = 46 AND active_s.store_id = %d
		WHERE ccp.product_id IN (%s)
		AND cce.level > 1
		ORDER BY cce.level ASC, cce.position ASC
	`, storeID, storeID, storeID, storeID, storeID, joinPlaceholders(placeholders))

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("category query failed: %w", err)
	}
	defer rows.Close()

	result := make(map[int][]*CategoryData)
	for rows.Next() {
		var productID int
		c := &CategoryData{}
		if err := rows.Scan(&productID, &c.EntityID, &c.ParentID, &c.Path, &c.Position, &c.Level,
			&c.Name, &c.URLKey, &c.URLPath, &c.Description, &c.IsActive); err != nil {
			return nil, fmt.Errorf("category scan failed: %w", err)
		}
		// Only include active categories
		if c.IsActive != nil && *c.IsActive != 1 {
			continue
		}
		result[productID] = append(result[productID], c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("category rows iteration failed: %w", err)
	}
	return result, nil
}

// BuildCategoryTree converts CategoryData to a GraphQL CategoryTree.
func BuildCategoryTree(c *CategoryData, urlSuffix string) *model.CategoryTree {
	uid := EncodeMagentoUID(c.EntityID)
	id := c.EntityID

	var canonicalURL *string
	if c.URLPath != nil {
		u := *c.URLPath + urlSuffix
		canonicalURL = &u
	}

	cat := &model.CategoryTree{
		ID:          &id,
		UID:         uid,
		Name:        c.Name,
		Description: strPtrDeref(c.Description),
		URLKey:      c.URLKey,
		URLPath:     c.URLPath,
		CanonicalURL: canonicalURL,
		Position:    &c.Position,
		Level:       &c.Level,
		Path:        &c.Path,
		URLSuffix:   &urlSuffix,
	}

	// Build breadcrumbs from path
	cat.Breadcrumbs = buildBreadcrumbs(c.Path, c.EntityID)

	return cat
}

func buildBreadcrumbs(path string, currentID int) []*model.Breadcrumb {
	parts := strings.Split(path, "/")
	if len(parts) <= 2 {
		return nil
	}
	// Skip root (1) and default category (2), and the current category
	var breadcrumbs []*model.Breadcrumb
	for _, part := range parts[2:] {
		catID, err := strconv.Atoi(part)
		if err != nil || catID == currentID {
			continue
		}
		uid := EncodeMagentoUID(catID)
		breadcrumbs = append(breadcrumbs, &model.Breadcrumb{
			CategoryID:  &catID,
			CategoryUID: uid,
		})
	}
	return breadcrumbs
}

func strPtrDeref(s *string) *string {
	if s == nil {
		return nil
	}
	if *s == "" {
		return nil
	}
	return s
}
