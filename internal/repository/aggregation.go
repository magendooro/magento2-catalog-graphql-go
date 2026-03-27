package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
)

// AggregationRepository handles faceted search / layered navigation aggregations.
type AggregationRepository struct {
	db                *sql.DB
	attrRepo          *AttributeRepository
	filterableAttrs   []*FilterableAttribute
	filterableAttrsMu sync.RWMutex
	filterableLoaded  bool
}

func NewAggregationRepository(db *sql.DB, attrRepo *AttributeRepository) *AggregationRepository {
	return &AggregationRepository{db: db, attrRepo: attrRepo}
}

// FilterableAttribute holds metadata for a filterable EAV attribute.
type FilterableAttribute struct {
	AttributeID   int
	AttributeCode string
	FrontendLabel string
	FrontendInput string
	Position      int
}

// AggregationBucket holds aggregation results for a single attribute.
type AggregationBucket struct {
	AttributeCode string
	Label         string
	Position      int
	Options       []*AggregationOption
}

// AggregationOption holds a single aggregation option with count.
type AggregationOption struct {
	Value string
	Label string
	Count int
}

// GetFilterableAttributes returns all filterable product attributes.
// For non-search queries (inSearch=false), results are cached after the first call.
func (r *AggregationRepository) GetFilterableAttributes(ctx context.Context, inSearch bool) ([]*FilterableAttribute, error) {
	// For the standard (non-search) case, serve from cache
	if !inSearch {
		r.filterableAttrsMu.RLock()
		if r.filterableLoaded {
			attrs := r.filterableAttrs
			r.filterableAttrsMu.RUnlock()
			return attrs, nil
		}
		r.filterableAttrsMu.RUnlock()
	}

	filterCol := "eaa.is_filterable"
	if inSearch {
		filterCol = "eaa.is_filterable_in_search"
	}

	query := `SELECT ea.attribute_id, ea.attribute_code, COALESCE(ea.frontend_label, ea.attribute_code),
		ea.frontend_input, eaa.position
		FROM eav_attribute ea
		JOIN catalog_eav_attribute eaa ON ea.attribute_id = eaa.attribute_id
		WHERE ea.entity_type_id = 4
		AND ` + filterCol + ` > 0
		ORDER BY eaa.position, ea.attribute_code`

	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("filterable attributes query failed: %w", err)
	}
	defer rows.Close()

	var attrs []*FilterableAttribute
	for rows.Next() {
		fa := &FilterableAttribute{}
		if err := rows.Scan(&fa.AttributeID, &fa.AttributeCode, &fa.FrontendLabel, &fa.FrontendInput, &fa.Position); err != nil {
			return nil, fmt.Errorf("filterable attributes scan failed: %w", err)
		}
		attrs = append(attrs, fa)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("filterable attributes rows iteration failed: %w", err)
	}

	// Cache the result for non-search queries
	if !inSearch {
		r.filterableAttrsMu.Lock()
		r.filterableAttrs = attrs
		r.filterableLoaded = true
		r.filterableAttrsMu.Unlock()
	}

	return attrs, nil
}

// GetSelectAggregations computes aggregation buckets for select/multiselect attributes
// using the catalog_product_index_eav table. Only counts products matching the given entity IDs.
func (r *AggregationRepository) GetSelectAggregations(ctx context.Context, attr *FilterableAttribute, matchingEntityIDs []int, storeID int) (*AggregationBucket, error) {
	if len(matchingEntityIDs) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(matchingEntityIDs))
	args := make([]interface{}, 0, len(matchingEntityIDs)+2)
	for i, id := range matchingEntityIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	args = append(args, attr.AttributeID)

	useStoreID := storeID
	if useStoreID == 0 {
		useStoreID = 0
	}
	args = append(args, useStoreID)

	query := `SELECT cpie.value as option_id,
		COALESCE(eaov_s.value, eaov_d.value, CAST(cpie.value AS CHAR)) as label,
		COUNT(DISTINCT cpie.entity_id) as cnt
		FROM catalog_product_index_eav cpie
		LEFT JOIN eav_attribute_option_value eaov_d ON cpie.value = eaov_d.option_id AND eaov_d.store_id = 0
		LEFT JOIN eav_attribute_option_value eaov_s ON cpie.value = eaov_s.option_id AND eaov_s.store_id = ?
		WHERE cpie.entity_id IN (` + strings.Join(placeholders, ",") + `)
		AND cpie.attribute_id = ?
		AND cpie.store_id = ?
		GROUP BY cpie.value, label
		HAVING cnt > 0
		ORDER BY cnt DESC`

	// Reorder args: entity_ids, then store_id for label join, then attribute_id, then store_id for filter
	labelStoreID := storeID
	reorderedArgs := make([]interface{}, 0, len(args)+1)
	reorderedArgs = append(reorderedArgs, labelStoreID) // for eaov_s join
	for _, id := range matchingEntityIDs {
		reorderedArgs = append(reorderedArgs, id)
	}
	reorderedArgs = append(reorderedArgs, attr.AttributeID)
	reorderedArgs = append(reorderedArgs, useStoreID)

	// Actually let me simplify the query to avoid confusion with arg ordering.
	// The label store join uses a constant, so let me use fmt.Sprintf for the store IDs.
	// Join through eav_attribute_option to filter by attribute_id,
	// ensuring we only get labels for options belonging to this specific attribute.
	query = fmt.Sprintf(`SELECT cpie.value as option_id,
		COALESCE(eaov_s.value, eaov_d.value, CAST(cpie.value AS CHAR)) as label,
		COUNT(DISTINCT cpie.entity_id) as cnt
		FROM catalog_product_index_eav cpie
		LEFT JOIN eav_attribute_option eao ON cpie.value = eao.option_id AND eao.attribute_id = ?
		LEFT JOIN eav_attribute_option_value eaov_d ON eao.option_id = eaov_d.option_id AND eaov_d.store_id = 0
		LEFT JOIN eav_attribute_option_value eaov_s ON eao.option_id = eaov_s.option_id AND eaov_s.store_id = %d
		WHERE cpie.entity_id IN (`+strings.Join(placeholders, ",")+`)
		AND cpie.attribute_id = ?
		AND cpie.store_id = %d
		GROUP BY cpie.value, label
		HAVING cnt > 0
		ORDER BY cnt DESC`, storeID, storeID)

	finalArgs := make([]interface{}, 0, len(matchingEntityIDs)+2)
	finalArgs = append(finalArgs, attr.AttributeID) // for eao.attribute_id join
	for _, id := range matchingEntityIDs {
		finalArgs = append(finalArgs, id)
	}
	finalArgs = append(finalArgs, attr.AttributeID) // for cpie.attribute_id filter

	rows, err := r.db.QueryContext(ctx, query, finalArgs...)
	if err != nil {
		return nil, fmt.Errorf("select aggregation query failed: %w", err)
	}
	defer rows.Close()

	bucket := &AggregationBucket{
		AttributeCode: attr.AttributeCode,
		Label:         attr.FrontendLabel,
		Position:      attr.Position,
	}

	for rows.Next() {
		var optionID int
		opt := &AggregationOption{}
		if err := rows.Scan(&optionID, &opt.Label, &opt.Count); err != nil {
			return nil, fmt.Errorf("select aggregation scan failed: %w", err)
		}
		opt.Value = fmt.Sprintf("%d", optionID)
		bucket.Options = append(bucket.Options, opt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("select aggregation rows iteration failed: %w", err)
	}

	if len(bucket.Options) == 0 {
		return nil, nil
	}
	return bucket, nil
}

// GetPriceAggregation computes price range aggregation using the price index.
func (r *AggregationRepository) GetPriceAggregation(ctx context.Context, matchingEntityIDs []int, websiteID int) (*AggregationBucket, error) {
	if len(matchingEntityIDs) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(matchingEntityIDs))
	args := make([]interface{}, len(matchingEntityIDs))
	for i, id := range matchingEntityIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	// Generate price ranges using buckets of automatic width
	query := `SELECT
		price_bucket * 50 as price_from,
		price_bucket * 50 + 49.99 as price_to,
		cnt
		FROM (
			SELECT FLOOR(pip.min_price / 50) as price_bucket,
				COUNT(DISTINCT pip.entity_id) as cnt
			FROM catalog_product_index_price pip
			WHERE pip.entity_id IN (` + strings.Join(placeholders, ",") + `)
			AND pip.customer_group_id = 0
			AND pip.website_id = ` + fmt.Sprintf("%d", websiteID) + `
			GROUP BY FLOOR(pip.min_price / 50)
			HAVING cnt > 0
		) t
		ORDER BY price_from`

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("price aggregation query failed: %w", err)
	}
	defer rows.Close()

	bucket := &AggregationBucket{
		AttributeCode: "price",
		Label:         "Price",
	}

	for rows.Next() {
		var priceFrom, priceTo float64
		opt := &AggregationOption{}
		if err := rows.Scan(&priceFrom, &priceTo, &opt.Count); err != nil {
			return nil, fmt.Errorf("price aggregation scan failed: %w", err)
		}
		opt.Value = fmt.Sprintf("%.0f_%.0f", priceFrom, priceTo+0.01)
		opt.Label = fmt.Sprintf("%.0f-%.0f", priceFrom, priceTo+0.01)
		bucket.Options = append(bucket.Options, opt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("price aggregation rows iteration failed: %w", err)
	}

	if len(bucket.Options) == 0 {
		return nil, nil
	}
	return bucket, nil
}

// GetCategoryAggregation computes category aggregation for products.
// scopeCategoryID, when > 0, restricts results to categories that are descendants of that
// category (path LIKE '{scope_path}/%'). This prevents cross-tree categories such as
// Collections/Eco Friendly from appearing when browsing Men or Women.
func (r *AggregationRepository) GetCategoryAggregation(ctx context.Context, matchingEntityIDs []int, storeID, scopeCategoryID int) (*AggregationBucket, error) {
	if len(matchingEntityIDs) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(matchingEntityIDs))
	args := make([]interface{}, 0, len(matchingEntityIDs)+1)
	for i, id := range matchingEntityIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}

	scopeClause := ""
	if scopeCategoryID > 0 {
		scopeClause = "AND cce.path LIKE CONCAT((SELECT path FROM catalog_category_entity WHERE entity_id = ?), '/%')"
		args = append(args, scopeCategoryID)
	}

	query := fmt.Sprintf(`SELECT ccp.category_id,
		COALESCE(ccevn_s.value, ccevn_d.value, CAST(ccp.category_id AS CHAR)) as label,
		COUNT(DISTINCT ccp.product_id) as cnt
		FROM catalog_category_product ccp
		JOIN catalog_category_entity cce ON ccp.category_id = cce.entity_id
		LEFT JOIN catalog_category_entity_varchar ccevn_d ON cce.entity_id = ccevn_d.entity_id
			AND ccevn_d.attribute_id = (SELECT attribute_id FROM eav_attribute WHERE attribute_code = 'name' AND entity_type_id = 3) AND ccevn_d.store_id = 0
		LEFT JOIN catalog_category_entity_varchar ccevn_s ON cce.entity_id = ccevn_s.entity_id
			AND ccevn_s.attribute_id = (SELECT attribute_id FROM eav_attribute WHERE attribute_code = 'name' AND entity_type_id = 3) AND ccevn_s.store_id = %d
		WHERE ccp.product_id IN (`+strings.Join(placeholders, ",")+`)
		AND cce.level > 1
		`+scopeClause+`
		GROUP BY ccp.category_id, label
		HAVING cnt > 0
		ORDER BY cnt DESC`, storeID)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("category aggregation query failed: %w", err)
	}
	defer rows.Close()

	bucket := &AggregationBucket{
		AttributeCode: "category_id",
		Label:         "Category",
	}

	for rows.Next() {
		var categoryID int
		opt := &AggregationOption{}
		if err := rows.Scan(&categoryID, &opt.Label, &opt.Count); err != nil {
			return nil, fmt.Errorf("category aggregation scan failed: %w", err)
		}
		opt.Value = fmt.Sprintf("%d", categoryID)
		bucket.Options = append(bucket.Options, opt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("category aggregation rows iteration failed: %w", err)
	}

	if len(bucket.Options) == 0 {
		return nil, nil
	}
	return bucket, nil
}

// ResolveOptionLabel resolves an option value to its label from eav_attribute_option_value.
func (r *AggregationRepository) ResolveOptionLabel(ctx context.Context, attributeID int, optionValue string, storeID int) (string, error) {
	var label string
	err := r.db.QueryRowContext(ctx, `
		SELECT COALESCE(store_val.value, default_val.value)
		FROM eav_attribute_option eao
		LEFT JOIN eav_attribute_option_value default_val ON eao.option_id = default_val.option_id AND default_val.store_id = 0
		LEFT JOIN eav_attribute_option_value store_val ON eao.option_id = store_val.option_id AND store_val.store_id = ?
		WHERE eao.attribute_id = ? AND eao.option_id = ?`,
		storeID, attributeID, optionValue,
	).Scan(&label)
	if err != nil {
		return "", err
	}
	return label, nil
}
