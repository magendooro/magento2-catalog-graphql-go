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
	EntityID      int
	ParentID      int
	Path          string
	Position      int
	Level         int
	Name          *string
	URLKey        *string
	URLPath       *string
	Description   *string
	IsActive      *int
	ChildrenCount int
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
	// Category EAV also uses entity_id in Magento EE
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
			COALESCE(active_s.value, active_d.value) AS is_active,
			cce.children_count
		FROM catalog_category_product ccp
		INNER JOIN catalog_category_entity cce ON ccp.category_id = cce.entity_id
		LEFT JOIN catalog_category_entity_varchar name_d ON cce.entity_id = name_d.entity_id AND name_d.attribute_id = (SELECT attribute_id FROM eav_attribute WHERE attribute_code = 'name' AND entity_type_id = 3) AND name_d.store_id = 0
		LEFT JOIN catalog_category_entity_varchar name_s ON cce.entity_id = name_s.entity_id AND name_s.attribute_id = (SELECT attribute_id FROM eav_attribute WHERE attribute_code = 'name' AND entity_type_id = 3) AND name_s.store_id = %d
		LEFT JOIN catalog_category_entity_varchar urlkey_d ON cce.entity_id = urlkey_d.entity_id AND urlkey_d.attribute_id = (SELECT attribute_id FROM eav_attribute WHERE attribute_code = 'url_key' AND entity_type_id = 3) AND urlkey_d.store_id = 0
		LEFT JOIN catalog_category_entity_varchar urlkey_s ON cce.entity_id = urlkey_s.entity_id AND urlkey_s.attribute_id = (SELECT attribute_id FROM eav_attribute WHERE attribute_code = 'url_key' AND entity_type_id = 3) AND urlkey_s.store_id = %d
		LEFT JOIN catalog_category_entity_varchar urlpath_d ON cce.entity_id = urlpath_d.entity_id AND urlpath_d.attribute_id = (SELECT attribute_id FROM eav_attribute WHERE attribute_code = 'url_path' AND entity_type_id = 3) AND urlpath_d.store_id = 0
		LEFT JOIN catalog_category_entity_varchar urlpath_s ON cce.entity_id = urlpath_s.entity_id AND urlpath_s.attribute_id = (SELECT attribute_id FROM eav_attribute WHERE attribute_code = 'url_path' AND entity_type_id = 3) AND urlpath_s.store_id = %d
		LEFT JOIN catalog_category_entity_text desc_d ON cce.entity_id = desc_d.entity_id AND desc_d.attribute_id = (SELECT attribute_id FROM eav_attribute WHERE attribute_code = 'description' AND entity_type_id = 3) AND desc_d.store_id = 0
		LEFT JOIN catalog_category_entity_text desc_s ON cce.entity_id = desc_s.entity_id AND desc_s.attribute_id = (SELECT attribute_id FROM eav_attribute WHERE attribute_code = 'description' AND entity_type_id = 3) AND desc_s.store_id = %d
		LEFT JOIN catalog_category_entity_int active_d ON cce.entity_id = active_d.entity_id AND active_d.attribute_id = (SELECT attribute_id FROM eav_attribute WHERE attribute_code = 'is_active' AND entity_type_id = 3) AND active_d.store_id = 0
		LEFT JOIN catalog_category_entity_int active_s ON cce.entity_id = active_s.entity_id AND active_s.attribute_id = (SELECT attribute_id FROM eav_attribute WHERE attribute_code = 'is_active' AND entity_type_id = 3) AND active_s.store_id = %d
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
			&c.Name, &c.URLKey, &c.URLPath, &c.Description, &c.IsActive, &c.ChildrenCount); err != nil {
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

// CategoryFilters holds parsed filter criteria for category queries.
type CategoryFilters struct {
	IDs       []int
	Name      *string
	ParentID  *int
	URLKey    *string
	URLPath   *string
}

// categoryBaseQuery returns the EAV-joined SELECT for category rows.
// storeID is interpolated directly (it's an internal integer, not user input).
func categoryBaseQuery(storeID int) string {
	return fmt.Sprintf(`
		SELECT
			cce.entity_id,
			cce.parent_id,
			cce.path,
			cce.position,
			cce.level,
			COALESCE(name_s.value, name_d.value) AS name,
			COALESCE(urlkey_s.value, urlkey_d.value) AS url_key,
			COALESCE(urlpath_s.value, urlpath_d.value) AS url_path,
			COALESCE(desc_s.value, desc_d.value) AS description,
			COALESCE(active_s.value, active_d.value) AS is_active,
			cce.children_count
		FROM catalog_category_entity cce
		LEFT JOIN catalog_category_entity_varchar name_d ON cce.entity_id = name_d.entity_id AND name_d.attribute_id = (SELECT attribute_id FROM eav_attribute WHERE attribute_code = 'name' AND entity_type_id = 3) AND name_d.store_id = 0
		LEFT JOIN catalog_category_entity_varchar name_s ON cce.entity_id = name_s.entity_id AND name_s.attribute_id = (SELECT attribute_id FROM eav_attribute WHERE attribute_code = 'name' AND entity_type_id = 3) AND name_s.store_id = %d
		LEFT JOIN catalog_category_entity_varchar urlkey_d ON cce.entity_id = urlkey_d.entity_id AND urlkey_d.attribute_id = (SELECT attribute_id FROM eav_attribute WHERE attribute_code = 'url_key' AND entity_type_id = 3) AND urlkey_d.store_id = 0
		LEFT JOIN catalog_category_entity_varchar urlkey_s ON cce.entity_id = urlkey_s.entity_id AND urlkey_s.attribute_id = (SELECT attribute_id FROM eav_attribute WHERE attribute_code = 'url_key' AND entity_type_id = 3) AND urlkey_s.store_id = %d
		LEFT JOIN catalog_category_entity_varchar urlpath_d ON cce.entity_id = urlpath_d.entity_id AND urlpath_d.attribute_id = (SELECT attribute_id FROM eav_attribute WHERE attribute_code = 'url_path' AND entity_type_id = 3) AND urlpath_d.store_id = 0
		LEFT JOIN catalog_category_entity_varchar urlpath_s ON cce.entity_id = urlpath_s.entity_id AND urlpath_s.attribute_id = (SELECT attribute_id FROM eav_attribute WHERE attribute_code = 'url_path' AND entity_type_id = 3) AND urlpath_s.store_id = %d
		LEFT JOIN catalog_category_entity_text desc_d ON cce.entity_id = desc_d.entity_id AND desc_d.attribute_id = (SELECT attribute_id FROM eav_attribute WHERE attribute_code = 'description' AND entity_type_id = 3) AND desc_d.store_id = 0
		LEFT JOIN catalog_category_entity_text desc_s ON cce.entity_id = desc_s.entity_id AND desc_s.attribute_id = (SELECT attribute_id FROM eav_attribute WHERE attribute_code = 'description' AND entity_type_id = 3) AND desc_s.store_id = %d
		LEFT JOIN catalog_category_entity_int active_d ON cce.entity_id = active_d.entity_id AND active_d.attribute_id = (SELECT attribute_id FROM eav_attribute WHERE attribute_code = 'is_active' AND entity_type_id = 3) AND active_d.store_id = 0
		LEFT JOIN catalog_category_entity_int active_s ON cce.entity_id = active_s.entity_id AND active_s.attribute_id = (SELECT attribute_id FROM eav_attribute WHERE attribute_code = 'is_active' AND entity_type_id = 3) AND active_s.store_id = %d
	`, storeID, storeID, storeID, storeID, storeID)
}

func scanCategoryRow(rows *sql.Rows) (*CategoryData, error) {
	c := &CategoryData{}
	if err := rows.Scan(&c.EntityID, &c.ParentID, &c.Path, &c.Position, &c.Level,
		&c.Name, &c.URLKey, &c.URLPath, &c.Description, &c.IsActive, &c.ChildrenCount); err != nil {
		return nil, err
	}
	return c, nil
}

// FindCategories returns a filtered, paginated list of active categories (level > 1).
func (r *CategoryRepository) FindCategories(ctx context.Context, filters CategoryFilters, pageSize, currentPage, storeID int) ([]*CategoryData, int, error) {
	base := categoryBaseQuery(storeID)

	var conditions []string
	var args []interface{}
	conditions = append(conditions, "cce.level > 1")
	conditions = append(conditions, "COALESCE(active_s.value, active_d.value) = 1")

	if len(filters.IDs) > 0 {
		ph := make([]string, len(filters.IDs))
		for i, id := range filters.IDs {
			ph[i] = "?"
			args = append(args, id)
		}
		conditions = append(conditions, "cce.entity_id IN ("+strings.Join(ph, ",")+")")
	}
	if filters.Name != nil {
		conditions = append(conditions, "COALESCE(name_s.value, name_d.value) LIKE ?")
		args = append(args, "%"+*filters.Name+"%")
	}
	if filters.ParentID != nil {
		conditions = append(conditions, "cce.parent_id = ?")
		args = append(args, *filters.ParentID)
	}
	if filters.URLKey != nil {
		conditions = append(conditions, "COALESCE(urlkey_s.value, urlkey_d.value) = ?")
		args = append(args, *filters.URLKey)
	}
	if filters.URLPath != nil {
		conditions = append(conditions, "COALESCE(urlpath_s.value, urlpath_d.value) = ?")
		args = append(args, *filters.URLPath)
	}

	where := " WHERE " + strings.Join(conditions, " AND ")
	orderBy := " ORDER BY cce.level ASC, cce.position ASC"

	// Count query: replace SELECT columns with COUNT(*), keep FROM+JOINs intact.
	fromIdx := strings.Index(base, "FROM catalog_category_entity")
	var totalCount int
	countQuery := "SELECT COUNT(*) " + base[fromIdx:] + where
	if err := r.db.QueryRowContext(ctx, countQuery, args...).Scan(&totalCount); err != nil {
		return nil, 0, fmt.Errorf("category count query failed: %w", err)
	}

	if pageSize <= 0 {
		pageSize = 20
	}
	if currentPage <= 0 {
		currentPage = 1
	}
	offset := (currentPage - 1) * pageSize
	query := base + where + orderBy + fmt.Sprintf(" LIMIT %d OFFSET %d", pageSize, offset)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("category list query failed: %w", err)
	}
	defer rows.Close()

	var result []*CategoryData
	for rows.Next() {
		c, err := scanCategoryRow(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("category scan failed: %w", err)
		}
		result = append(result, c)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("category rows iteration failed: %w", err)
	}
	return result, totalCount, nil
}

// GetCategoryByID returns a single active category by its entity_id.
func (r *CategoryRepository) GetCategoryByID(ctx context.Context, entityID, storeID int) (*CategoryData, error) {
	query := categoryBaseQuery(storeID) + " WHERE cce.entity_id = ?"
	rows, err := r.db.QueryContext(ctx, query, entityID)
	if err != nil {
		return nil, fmt.Errorf("get category by id query failed: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		c, err := scanCategoryRow(rows)
		if err != nil {
			return nil, fmt.Errorf("category scan failed: %w", err)
		}
		if rows.Err() != nil {
			return nil, rows.Err()
		}
		return c, nil
	}
	return nil, rows.Err()
}

// GetChildCategories returns direct active children of the given parent category.
func (r *CategoryRepository) GetChildCategories(ctx context.Context, parentID, storeID int) ([]*CategoryData, error) {
	query := categoryBaseQuery(storeID) +
		" WHERE cce.parent_id = ? AND COALESCE(active_s.value, active_d.value) = 1" +
		" ORDER BY cce.position ASC"
	rows, err := r.db.QueryContext(ctx, query, parentID)
	if err != nil {
		return nil, fmt.Errorf("get child categories query failed: %w", err)
	}
	defer rows.Close()

	var result []*CategoryData
	for rows.Next() {
		c, err := scanCategoryRow(rows)
		if err != nil {
			return nil, fmt.Errorf("category scan failed: %w", err)
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

// GetCategoryName resolves a category name by entity_id and store_id.
func (r *CategoryRepository) GetCategoryName(ctx context.Context, categoryID, storeID int) (string, error) {
	var name string
	err := r.db.QueryRowContext(ctx, `
		SELECT COALESCE(store_val.value, default_val.value)
		FROM catalog_category_entity cce
		LEFT JOIN catalog_category_entity_varchar default_val
			ON cce.entity_id = default_val.entity_id
			AND default_val.attribute_id = (SELECT attribute_id FROM eav_attribute WHERE attribute_code = 'name' AND entity_type_id = 3)
			AND default_val.store_id = 0
		LEFT JOIN catalog_category_entity_varchar store_val
			ON cce.entity_id = store_val.entity_id
			AND store_val.attribute_id = (SELECT attribute_id FROM eav_attribute WHERE attribute_code = 'name' AND entity_type_id = 3)
			AND store_val.store_id = ?
		WHERE cce.entity_id = ?`,
		storeID, categoryID,
	).Scan(&name)
	if err != nil {
		return "", err
	}
	return name, nil
}
