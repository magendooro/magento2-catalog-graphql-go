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
	Value      string
	Label      string
	Count      int
	SwatchType *int    // nil when no swatch; 0=text, 1=color, 2=image
	SwatchValue *string // hex code (type 1), image path (type 2), text (type 0)
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

	query := `SELECT ea.attribute_id, ea.attribute_code,
		COALESCE(eal.value, ea.frontend_label, ea.attribute_code),
		ea.frontend_input, eaa.position
		FROM eav_attribute ea
		JOIN catalog_eav_attribute eaa ON ea.attribute_id = eaa.attribute_id
		LEFT JOIN eav_attribute_label eal ON ea.attribute_id = eal.attribute_id AND eal.store_id = 1
		WHERE ea.entity_type_id = 4
		AND ` + filterCol + ` > 0
		ORDER BY eaa.position ASC, ea.attribute_id ASC`

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
	for i := range matchingEntityIDs {
		placeholders[i] = "?"
	}
	inClause := strings.Join(placeholders, ",")

	// Use a UNION of two sources to find attribute values:
	// 1. catalog_product_index_eav — the flat EAV index (most filterable attributes)
	// 2. catalog_product_super_link + catalog_product_entity_int — configurable super
	//    attributes (color, size) stored on child products, mapped back to parent_id
	//    so the count reflects distinct parent products.
	//
	// swatch_type / swatch_value are fetched from eav_attribute_option_swatch so the
	// frontend can render colour swatches for the "color" attribute without a separate request.
	query := fmt.Sprintf(`SELECT src.option_id,
		COALESCE(eaov_s.value, eaov_d.value, CAST(src.option_id AS CHAR)) as label,
		COUNT(DISTINCT src.parent_id) as cnt,
		eas.type as swatch_type,
		eas.value as swatch_value
		FROM (
			SELECT cpie.entity_id as parent_id, cpie.value as option_id
			FROM catalog_product_index_eav cpie
			WHERE cpie.entity_id IN (%s)
			AND cpie.attribute_id = ?
			AND cpie.store_id = %d
			UNION
			SELECT cpsl.parent_id, cpei.value as option_id
			FROM catalog_product_super_link cpsl
			INNER JOIN catalog_product_entity_int cpei ON cpsl.product_id = cpei.entity_id
			WHERE cpsl.parent_id IN (%s)
			AND cpei.attribute_id = ?
			AND cpei.store_id = 0
		) src
		LEFT JOIN eav_attribute_option eao ON src.option_id = eao.option_id AND eao.attribute_id = ?
		LEFT JOIN eav_attribute_option_value eaov_d ON eao.option_id = eaov_d.option_id AND eaov_d.store_id = 0
		LEFT JOIN eav_attribute_option_value eaov_s ON eao.option_id = eaov_s.option_id AND eaov_s.store_id = %d
		LEFT JOIN eav_attribute_option_swatch eas ON src.option_id = eas.option_id AND eas.store_id = 0
		GROUP BY src.option_id, label, eas.type, eas.value
		HAVING cnt > 0
		ORDER BY label ASC`, inClause, storeID, inClause, storeID)

	finalArgs := make([]interface{}, 0, len(matchingEntityIDs)*2+3)
	// First IN clause (catalog_product_index_eav)
	for _, id := range matchingEntityIDs {
		finalArgs = append(finalArgs, id)
	}
	finalArgs = append(finalArgs, attr.AttributeID)
	// Second IN clause (catalog_product_super_link)
	for _, id := range matchingEntityIDs {
		finalArgs = append(finalArgs, id)
	}
	finalArgs = append(finalArgs, attr.AttributeID)
	// JOIN clause
	finalArgs = append(finalArgs, attr.AttributeID)

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
		var swatchType sql.NullInt64
		var swatchValue sql.NullString
		opt := &AggregationOption{}
		if err := rows.Scan(&optionID, &opt.Label, &opt.Count, &swatchType, &swatchValue); err != nil {
			return nil, fmt.Errorf("select aggregation scan failed: %w", err)
		}
		opt.Value = fmt.Sprintf("%d", optionID)
		if swatchType.Valid && swatchValue.Valid && swatchValue.String != "" {
			t := int(swatchType.Int64)
			opt.SwatchType = &t
			opt.SwatchValue = &swatchValue.String
		}
		// Boolean attributes store 0/1 but Magento displays "No"/"Yes"
		if attr.FrontendInput == "boolean" {
			if opt.Value == "1" {
				opt.Label = "Yes"
			} else {
				opt.Label = "No"
			}
		}
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
		price_bucket * 10 as price_from,
		price_bucket * 10 + 9.99 as price_to,
		cnt
		FROM (
			SELECT FLOOR(pip.min_price / 10) as price_bucket,
				COUNT(DISTINCT pip.entity_id) as cnt
			FROM catalog_product_index_price pip
			WHERE pip.entity_id IN (` + strings.Join(placeholders, ",") + `)
			AND pip.customer_group_id = 0
			AND pip.website_id = ` + fmt.Sprintf("%d", websiteID) + `
			GROUP BY FLOOR(pip.min_price / 10)
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
