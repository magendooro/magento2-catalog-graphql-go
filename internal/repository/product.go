package repository

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/magendooro/magento2-catalog-graphql-go/graph/model"
)

// ProductRepository handles product data queries against the Magento database.
type ProductRepository struct {
	db       *sql.DB
	attrRepo *AttributeRepository
}

func NewProductRepository(db *sql.DB, attrRepo *AttributeRepository) *ProductRepository {
	return &ProductRepository{db: db, attrRepo: attrRepo}
}

// ProductEAVValues holds flattened EAV attribute values for a product.
type ProductEAVValues struct {
	EntityID         int
	RowID            int
	SKU              string
	TypeID           string
	AttributeSetID   int
	CreatedAt        string
	UpdatedAt        string
	Name             *string
	Description      *string
	ShortDescription *string
	URLKey           *string
	MetaTitle        *string
	MetaKeyword      *string
	MetaDescription  *string
	Image            *string
	SmallImage       *string
	Thumbnail        *string
	SwatchImage      *string
	SpecialPrice     *float64
	SpecialFromDate  *string
	SpecialToDate    *string
	NewFromDate      *string
	NewToDate        *string
	Weight           *float64
	Status           *int
	Visibility       *int
	Manufacturer     *int
	CountryOfMfg     *string
	OptionsContainer *string
	GiftMsgAvail     *string
}

// eavJoinDef defines an EAV attribute JOIN for the product query.
type eavJoinDef struct {
	alias string
	code  string
}

// coreEAVAttributes defines the EAV attributes we JOIN for every product query.
var coreEAVAttributes = []eavJoinDef{
	{"pname", "name"},
	{"pdesc", "description"},
	{"sdesc", "short_description"},
	{"urlkey", "url_key"},
	{"mtitle", "meta_title"},
	{"mkw", "meta_keyword"},
	{"mdesc", "meta_description"},
	{"img", "image"},
	{"simg", "small_image"},
	{"thumb", "thumbnail"},
	{"sprice", "special_price"},
	{"spfrom", "special_from_date"},
	{"spto", "special_to_date"},
	{"nfrom", "news_from_date"},
	{"nto", "news_to_date"},
	{"weight", "weight"},
	{"status", "status"},
	{"vis", "visibility"},
	{"mfr", "manufacturer"},
	{"com", "country_of_manufacture"},
	{"oc", "options_container"},
	{"gma", "gift_message_available"},
	{"swimg", "swatch_image"},
}

// FindProducts queries products with EAV attributes resolved via optimized JOINs.
// Store-scoped with fallback: store-specific value → default (store_id=0).
func (r *ProductRepository) FindProducts(ctx context.Context, storeID int, search *string, filter *model.ProductAttributeFilterInput, sort *model.ProductAttributeSortInput, pageSize, currentPage int) ([]*ProductEAVValues, int, []int, error) {
	// Build SELECT columns
	selectCols := []string{
		"cpe.entity_id", "cpe.entity_id", "cpe.sku", "cpe.type_id", "cpe.attribute_set_id",
		"cpe.created_at", "cpe.updated_at",
	}

	var joinBuilder strings.Builder
	joinBuilder.WriteString("FROM catalog_product_entity cpe\n")

	for _, attr := range coreEAVAttributes {
		meta := r.attrRepo.Get(attr.code)
		if meta == nil {
			// Attribute doesn't exist in this installation, use NULL
			selectCols = append(selectCols, "NULL as "+attr.alias)
			continue
		}

		table := TableForType(meta.BackendType)
		if table == "" {
			selectCols = append(selectCols, "NULL as "+attr.alias)
			continue
		}

		// Default store JOIN
		defaultAlias := attr.alias + "_d"
		fmt.Fprintf(&joinBuilder,
			"LEFT JOIN %s %s ON cpe.entity_id = %s.entity_id AND %s.attribute_id = %d AND %s.store_id = 0\n",
			table, defaultAlias, defaultAlias, defaultAlias, meta.AttributeID, defaultAlias,
		)

		if storeID > 0 {
			storeAlias := attr.alias + "_s"
			fmt.Fprintf(&joinBuilder,
				"LEFT JOIN %s %s ON cpe.entity_id = %s.entity_id AND %s.attribute_id = %d AND %s.store_id = %d\n",
				table, storeAlias, storeAlias, storeAlias, meta.AttributeID, storeAlias, storeID,
			)
			selectCols = append(selectCols, fmt.Sprintf("COALESCE(%s.value, %s.value) as %s", storeAlias, defaultAlias, attr.alias))
		} else {
			selectCols = append(selectCols, fmt.Sprintf("%s.value as %s", defaultAlias, attr.alias))
		}
	}

	// Build the base query (without SELECT prefix yet — we'll add it for both the data and count queries)
	fromClause := joinBuilder.String()
	query := "SELECT " + strings.Join(selectCols, ", ") + "\n" + fromClause

	// Build filter conditions
	conditions, args, extraJoin := r.buildFilterConditions(storeID, search, filter)
	if extraJoin != "" {
		query += extraJoin
	}

	// Add price sort JOIN if needed (must come before WHERE)
	if sort != nil && sort.Price != nil {
		if filter == nil || filter.Price == nil {
			// Only add if not already added by price filter
			query += fmt.Sprintf("LEFT JOIN catalog_product_index_price pip_sort ON cpe.entity_id = pip_sort.entity_id AND pip_sort.customer_group_id = 0 AND pip_sort.website_id = (SELECT website_id FROM store WHERE store_id = %d LIMIT 1)\n", storeID)
		}
	}

	if len(conditions) > 0 {
		query += "WHERE " + strings.Join(conditions, " AND ") + "\n"
	}

	// Sorting
	hasPriceFilterJoin := filter != nil && filter.Price != nil
	orderBy := r.buildOrderBy(sort, storeID, hasPriceFilterJoin)
	if orderBy != "" {
		query += "ORDER BY " + orderBy + "\n"
	}

	// Pagination
	offset := (currentPage - 1) * pageSize
	query += fmt.Sprintf("LIMIT %d OFFSET %d", pageSize, offset)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("product query failed: %w", err)
	}
	defer rows.Close()

	var products []*ProductEAVValues
	for rows.Next() {
		p := &ProductEAVValues{}
		err := rows.Scan(
			&p.EntityID, &p.RowID, &p.SKU, &p.TypeID, &p.AttributeSetID,
			&p.CreatedAt, &p.UpdatedAt,
			&p.Name, &p.Description, &p.ShortDescription,
			&p.URLKey, &p.MetaTitle, &p.MetaKeyword, &p.MetaDescription,
			&p.Image, &p.SmallImage, &p.Thumbnail,
			&p.SpecialPrice, &p.SpecialFromDate, &p.SpecialToDate,
			&p.NewFromDate, &p.NewToDate,
			&p.Weight, &p.Status, &p.Visibility,
			&p.Manufacturer, &p.CountryOfMfg,
			&p.OptionsContainer, &p.GiftMsgAvail,
			&p.SwatchImage,
		)
		if err != nil {
			return nil, 0, nil, fmt.Errorf("scan failed: %w", err)
		}
		products = append(products, p)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, nil, fmt.Errorf("rows iteration failed: %w", err)
	}

	// Get all matching entity IDs (unpaginated) for totalCount and aggregation reuse.
	// This replaces both the old COUNT(*) query and the separate FindMatchingEntityIDs call,
	// avoiding a duplicate full-table scan.
	matchQuery := "SELECT DISTINCT cpe.entity_id\n" + fromClause
	if extraJoin != "" {
		matchQuery += extraJoin
	}
	if len(conditions) > 0 {
		matchQuery += "WHERE " + strings.Join(conditions, " AND ")
	}
	matchRows, err := r.db.QueryContext(ctx, matchQuery, args...)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("matching IDs query failed: %w", err)
	}
	defer matchRows.Close()

	var allMatchingIDs []int
	for matchRows.Next() {
		var id int
		if err := matchRows.Scan(&id); err != nil {
			return nil, 0, nil, fmt.Errorf("matching IDs scan failed: %w", err)
		}
		allMatchingIDs = append(allMatchingIDs, id)
	}
	if err := matchRows.Err(); err != nil {
		return nil, 0, nil, fmt.Errorf("matching IDs rows iteration failed: %w", err)
	}
	totalCount := len(allMatchingIDs)

	return products, totalCount, allMatchingIDs, nil
}

// FindProductsByIDs fetches products by a specific list of entity_ids, preserving the input order.
// Used when OpenSearch provides entity_ids in relevance order.
func (r *ProductRepository) FindProductsByIDs(ctx context.Context, storeID int, entityIDs []int, totalCount int) ([]*ProductEAVValues, int, []int, error) {
	if len(entityIDs) == 0 {
		return nil, totalCount, nil, nil
	}

	// Build SELECT columns (same as FindProducts)
	selectCols := []string{
		"cpe.entity_id", "cpe.entity_id", "cpe.sku", "cpe.type_id", "cpe.attribute_set_id",
		"cpe.created_at", "cpe.updated_at",
	}

	var joinBuilder strings.Builder
	joinBuilder.WriteString("FROM catalog_product_entity cpe\n")

	for _, attr := range coreEAVAttributes {
		meta := r.attrRepo.Get(attr.code)
		if meta == nil {
			selectCols = append(selectCols, "NULL as "+attr.alias)
			continue
		}

		table := TableForType(meta.BackendType)
		if table == "" {
			selectCols = append(selectCols, "NULL as "+attr.alias)
			continue
		}

		defaultAlias := attr.alias + "_d"
		fmt.Fprintf(&joinBuilder,
			"LEFT JOIN %s %s ON cpe.entity_id = %s.entity_id AND %s.attribute_id = %d AND %s.store_id = 0\n",
			table, defaultAlias, defaultAlias, defaultAlias, meta.AttributeID, defaultAlias,
		)

		if storeID > 0 {
			storeAlias := attr.alias + "_s"
			fmt.Fprintf(&joinBuilder,
				"LEFT JOIN %s %s ON cpe.entity_id = %s.entity_id AND %s.attribute_id = %d AND %s.store_id = %d\n",
				table, storeAlias, storeAlias, storeAlias, meta.AttributeID, storeAlias, storeID,
			)
			selectCols = append(selectCols, fmt.Sprintf("COALESCE(%s.value, %s.value) as %s", storeAlias, defaultAlias, attr.alias))
		} else {
			selectCols = append(selectCols, fmt.Sprintf("%s.value as %s", defaultAlias, attr.alias))
		}
	}

	// Build placeholders and ORDER BY FIELD to preserve ES relevance order
	placeholders := make([]string, len(entityIDs))
	args := make([]interface{}, 0, len(entityIDs)*2)
	for i, id := range entityIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}

	whereClause := fmt.Sprintf("WHERE cpe.entity_id IN (%s)", strings.Join(placeholders, ","))
	orderClause := "ORDER BY FIELD(cpe.entity_id, " + strings.Join(placeholders, ",") + ")"
	// Add entity IDs again for the FIELD() function
	for _, id := range entityIDs {
		args = append(args, id)
	}

	query := "SELECT " + strings.Join(selectCols, ", ") + "\n" + joinBuilder.String() + "\n" + whereClause + "\n" + orderClause

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("find products by IDs: %w", err)
	}
	defer rows.Close()

	var products []*ProductEAVValues
	for rows.Next() {
		p := &ProductEAVValues{}
		err := rows.Scan(
			&p.EntityID, &p.RowID, &p.SKU, &p.TypeID, &p.AttributeSetID,
			&p.CreatedAt, &p.UpdatedAt,
			&p.Name, &p.Description, &p.ShortDescription,
			&p.URLKey, &p.MetaTitle, &p.MetaKeyword, &p.MetaDescription,
			&p.Image, &p.SmallImage, &p.Thumbnail,
			&p.SpecialPrice, &p.SpecialFromDate, &p.SpecialToDate,
			&p.NewFromDate, &p.NewToDate,
			&p.Weight, &p.Status, &p.Visibility,
			&p.Manufacturer, &p.CountryOfMfg,
			&p.OptionsContainer, &p.GiftMsgAvail,
			&p.SwatchImage,
		)
		if err != nil {
			return nil, 0, nil, fmt.Errorf("scan product by ID: %w", err)
		}
		products = append(products, p)
	}

	return products, totalCount, entityIDs, rows.Err()
}

// FindMatchingEntityIDs returns all entity IDs matching the given filter (no pagination).
// Used for aggregation context.
func (r *ProductRepository) FindMatchingEntityIDs(ctx context.Context, storeID int, search *string, filter *model.ProductAttributeFilterInput) ([]int, error) {
	// We need the EAV JOINs for status and visibility filtering
	var joinBuilder strings.Builder
	joinBuilder.WriteString("FROM catalog_product_entity cpe\n")

	// Only join the attributes needed for filtering (status, visibility, name, urlkey, + description for search)
	neededAttrs := []eavJoinDef{
		{"pname", "name"},
		{"urlkey", "url_key"},
		{"status", "status"},
		{"vis", "visibility"},
	}
	if search != nil && *search != "" {
		neededAttrs = append(neededAttrs,
			eavJoinDef{"pdesc", "description"},
			eavJoinDef{"sdesc", "short_description"},
		)
	}

	for _, attr := range neededAttrs {
		meta := r.attrRepo.Get(attr.code)
		if meta == nil {
			continue
		}
		table := TableForType(meta.BackendType)
		if table == "" {
			continue
		}
		defaultAlias := attr.alias + "_d"
		fmt.Fprintf(&joinBuilder,
			"LEFT JOIN %s %s ON cpe.entity_id = %s.entity_id AND %s.attribute_id = %d AND %s.store_id = 0\n",
			table, defaultAlias, defaultAlias, defaultAlias, meta.AttributeID, defaultAlias,
		)
		if storeID > 0 {
			storeAlias := attr.alias + "_s"
			fmt.Fprintf(&joinBuilder,
				"LEFT JOIN %s %s ON cpe.entity_id = %s.entity_id AND %s.attribute_id = %d AND %s.store_id = %d\n",
				table, storeAlias, storeAlias, storeAlias, meta.AttributeID, storeAlias, storeID,
			)
		}
	}

	query := "SELECT DISTINCT cpe.entity_id\n" + joinBuilder.String()

	conditions, args, extraJoin := r.buildFilterConditions(storeID, search, filter)
	if extraJoin != "" {
		query += extraJoin
	}

	if len(conditions) > 0 {
		query += "WHERE " + strings.Join(conditions, " AND ")
	}

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("matching entity IDs query failed: %w", err)
	}
	defer rows.Close()

	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("matching entity IDs scan failed: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("matching entity IDs rows iteration failed: %w", err)
	}
	return ids, nil
}

// buildFilterConditions builds WHERE conditions and args from filter input.
// Returns conditions, args, and any extra JOIN clauses needed (e.g., price filter).
func (r *ProductRepository) buildFilterConditions(storeID int, search *string, filter *model.ProductAttributeFilterInput) (conditions []string, args []interface{}, extraJoin string) {
	// Always filter: enabled + visible in catalog
	conditions = append(conditions, r.coalesceCol("status", storeID)+" = 1")
	conditions = append(conditions, r.coalesceCol("vis", storeID)+" IN (2, 3, 4)")

	// Full-text search: LIKE on name, sku, description
	if search != nil && *search != "" {
		searchTerm := "%" + *search + "%"
		conditions = append(conditions, "(cpe.sku LIKE ? OR "+r.coalesceCol("pname", storeID)+" LIKE ? OR "+r.coalesceCol("pdesc", storeID)+" LIKE ? OR "+r.coalesceCol("sdesc", storeID)+" LIKE ?)")
		args = append(args, searchTerm, searchTerm, searchTerm, searchTerm)
	}

	if filter == nil {
		return
	}

	if filter.Sku != nil {
		if filter.Sku.Eq != nil {
			conditions = append(conditions, "cpe.sku = ?")
			args = append(args, *filter.Sku.Eq)
		}
		if len(filter.Sku.In) > 0 {
			placeholders := make([]string, len(filter.Sku.In))
			for i, v := range filter.Sku.In {
				placeholders[i] = "?"
				args = append(args, *v)
			}
			conditions = append(conditions, "cpe.sku IN ("+strings.Join(placeholders, ",")+")")
		}
	}
	if filter.Name != nil && filter.Name.Match != nil {
		conditions = append(conditions, r.coalesceCol("pname", storeID)+" LIKE ?")
		args = append(args, "%"+*filter.Name.Match+"%")
	}
	if filter.URLKey != nil {
		if filter.URLKey.Eq != nil {
			conditions = append(conditions, r.coalesceCol("urlkey", storeID)+" = ?")
			args = append(args, *filter.URLKey.Eq)
		}
		if len(filter.URLKey.In) > 0 {
			placeholders := make([]string, len(filter.URLKey.In))
			for i, v := range filter.URLKey.In {
				placeholders[i] = "?"
				args = append(args, *v)
			}
			conditions = append(conditions, r.coalesceCol("urlkey", storeID)+" IN ("+strings.Join(placeholders, ",")+")")
		}
	}
	if filter.CategoryID != nil {
		if filter.CategoryID.Eq != nil {
			conditions = append(conditions, "cpe.entity_id IN (SELECT product_id FROM catalog_category_product WHERE category_id = ?)")
			args = append(args, *filter.CategoryID.Eq)
		}
		if len(filter.CategoryID.In) > 0 {
			placeholders := make([]string, len(filter.CategoryID.In))
			for i, v := range filter.CategoryID.In {
				placeholders[i] = "?"
				args = append(args, *v)
			}
			conditions = append(conditions, "cpe.entity_id IN (SELECT product_id FROM catalog_category_product WHERE category_id IN ("+strings.Join(placeholders, ",")+"))")
		}
	}
	if filter.CategoryUID != nil {
		if filter.CategoryUID.Eq != nil {
			catID := decodeMagentoUID(*filter.CategoryUID.Eq)
			conditions = append(conditions, "cpe.entity_id IN (SELECT product_id FROM catalog_category_product WHERE category_id = ?)")
			args = append(args, catID)
		}
		if len(filter.CategoryUID.In) > 0 {
			placeholders := make([]string, len(filter.CategoryUID.In))
			for i, v := range filter.CategoryUID.In {
				placeholders[i] = "?"
				args = append(args, decodeMagentoUID(*v))
			}
			conditions = append(conditions, "cpe.entity_id IN (SELECT product_id FROM catalog_category_product WHERE category_id IN ("+strings.Join(placeholders, ",")+"))")
		}
	}
	if filter.CategoryURLPath != nil {
		if filter.CategoryURLPath.Eq != nil {
			conditions = append(conditions, `cpe.entity_id IN (
				SELECT ccp.product_id FROM catalog_category_product ccp
				JOIN catalog_category_entity cce ON ccp.category_id = cce.entity_id
				JOIN catalog_category_entity_varchar ccev ON cce.entity_id = ccev.entity_id
					AND ccev.attribute_id = (SELECT attribute_id FROM eav_attribute WHERE attribute_code = 'url_path' AND entity_type_id = 3)
					AND ccev.store_id = 0
				WHERE ccev.value = ?
			)`)
			args = append(args, *filter.CategoryURLPath.Eq)
		}
	}
	if filter.Price != nil {
		extraJoin = fmt.Sprintf("INNER JOIN catalog_product_index_price pip ON cpe.entity_id = pip.entity_id AND pip.customer_group_id = 0 AND pip.website_id = (SELECT website_id FROM store WHERE store_id = %d LIMIT 1)\n", storeID)
		if filter.Price.From != nil {
			conditions = append(conditions, "pip.min_price >= ?")
			args = append(args, *filter.Price.From)
		}
		if filter.Price.To != nil {
			conditions = append(conditions, "pip.min_price <= ?")
			args = append(args, *filter.Price.To)
		}
	}

	return
}

// buildOrderBy constructs the ORDER BY clause from sort input.
func (r *ProductRepository) buildOrderBy(sort *model.ProductAttributeSortInput, storeID int, hasPriceFilterJoin bool) string {
	if sort == nil {
		return "cpe.entity_id DESC"
	}

	var parts []string

	if sort.Name != nil {
		dir := "ASC"
		if *sort.Name == model.SortEnumDesc {
			dir = "DESC"
		}
		parts = append(parts, r.coalesceCol("pname", storeID)+" "+dir)
	}
	if sort.Price != nil {
		dir := "ASC"
		if *sort.Price == model.SortEnumDesc {
			dir = "DESC"
		}
		priceAlias := "pip_sort"
		if hasPriceFilterJoin {
			priceAlias = "pip"
		}
		parts = append(parts, priceAlias+".min_price "+dir)
	}
	if sort.Position != nil {
		dir := "ASC"
		if *sort.Position == model.SortEnumDesc {
			dir = "DESC"
		}
		parts = append(parts, "cpe.entity_id "+dir) // position sort needs category context; fallback to entity_id
	}
	if sort.Relevance != nil {
		// Relevance sorting is only meaningful with search; default to entity_id
		dir := "ASC"
		if *sort.Relevance == model.SortEnumDesc {
			dir = "DESC"
		}
		parts = append(parts, "cpe.entity_id "+dir)
	}

	if len(parts) == 0 {
		return "cpe.entity_id DESC"
	}
	// Add entity_id DESC as secondary sort for stable tie-breaking (matches Magento/OpenSearch)
	parts = append(parts, "cpe.entity_id DESC")
	return strings.Join(parts, ", ")
}

// coalesceCol builds the COALESCE expression for a store-scoped EAV attribute.
func (r *ProductRepository) coalesceCol(alias string, storeID int) string {
	if storeID > 0 {
		return fmt.Sprintf("COALESCE(%s_s.value, %s_d.value)", alias, alias)
	}
	return alias + "_d.value"
}

// decodeMagentoUID decodes a base64-encoded Magento UID back to the raw value.
func decodeMagentoUID(uid string) string {
	decoded, err := base64.StdEncoding.DecodeString(uid)
	if err != nil {
		return uid
	}
	return string(decoded)
}

// EncodeMagentoUID encodes an entity ID to a Magento-compatible base64 UID.
func EncodeMagentoUID(entityID int) string {
	return base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%d", entityID)))
}

// EncodeConfigurableUID encodes a configurable option UID as Magento does:
// base64("configurable/{attribute_id}/{value_index}")
func EncodeConfigurableUID(attributeID, valueIndex int) string {
	return base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("configurable/%d/%d", attributeID, valueIndex)))
}
