package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// ConfigurableRepository handles configurable product data (super attributes, variants, swatches).
type ConfigurableRepository struct {
	db       *sql.DB
	attrRepo *AttributeRepository
}

func NewConfigurableRepository(db *sql.DB, attrRepo *AttributeRepository) *ConfigurableRepository {
	return &ConfigurableRepository{db: db, attrRepo: attrRepo}
}

// SuperAttributeData holds a configurable product's super attribute definition.
type SuperAttributeData struct {
	ProductSuperAttributeID int
	ProductID               int // parent entity_id
	AttributeID             int
	AttributeCode           string
	Label                   string
	Position                int
	UseDefault              bool
}

// SuperLinkData holds a parent→child link for configurable products.
type SuperLinkData struct {
	ParentID      int // parent entity_id
	ChildEntityID int // child entity_id
}

// AttributeOptionData holds an option value for an EAV attribute.
type AttributeOptionData struct {
	OptionID int
	Label    string
}

// SwatchData holds swatch information for an attribute option.
type SwatchData struct {
	OptionID  int
	SwatchType int // 0=none, 1=color, 2=image, 3=text
	Value     string
}

// ChildAttributeValue holds the value of a super attribute for a child product.
type ChildAttributeValue struct {
	ChildEntityID int
	AttributeID   int
	ValueInt      int // the option_id stored in the int table
}

// GetSuperAttributesForProducts batch-loads super attribute definitions for configurable products.
// Returns map[parentEntityID][]SuperAttributeData.
func (r *ConfigurableRepository) GetSuperAttributesForProducts(ctx context.Context, parentEntityIDs []int, storeID int) (map[int][]*SuperAttributeData, error) {
	if len(parentEntityIDs) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(parentEntityIDs))
	args := make([]interface{}, len(parentEntityIDs))
	for i, id := range parentEntityIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	// store-scoped label with fallback to default
	query := `
		SELECT cpsa.product_super_attribute_id, cpsa.product_id, cpsa.attribute_id,
			ea.attribute_code, cpsa.position,
			COALESCE(cpsal_s.value, cpsal_d.value, ea.frontend_label) as label,
			COALESCE(cpsal_d.use_default, 0) as use_default
		FROM catalog_product_super_attribute cpsa
		JOIN eav_attribute ea ON cpsa.attribute_id = ea.attribute_id
		LEFT JOIN catalog_product_super_attribute_label cpsal_d
			ON cpsa.product_super_attribute_id = cpsal_d.product_super_attribute_id AND cpsal_d.store_id = 0`

	if storeID > 0 {
		query += fmt.Sprintf(`
		LEFT JOIN catalog_product_super_attribute_label cpsal_s
			ON cpsa.product_super_attribute_id = cpsal_s.product_super_attribute_id AND cpsal_s.store_id = %d`, storeID)
	} else {
		// No store-specific join needed, but we referenced cpsal_s in COALESCE
		// Simplify: just use the default
		query = `
		SELECT cpsa.product_super_attribute_id, cpsa.product_id, cpsa.attribute_id,
			ea.attribute_code, cpsa.position,
			COALESCE(cpsal_d.value, ea.frontend_label) as label,
			COALESCE(cpsal_d.use_default, 0) as use_default
		FROM catalog_product_super_attribute cpsa
		JOIN eav_attribute ea ON cpsa.attribute_id = ea.attribute_id
		LEFT JOIN catalog_product_super_attribute_label cpsal_d
			ON cpsa.product_super_attribute_id = cpsal_d.product_super_attribute_id AND cpsal_d.store_id = 0`
	}

	query += `
		WHERE cpsa.product_id IN (` + strings.Join(placeholders, ",") + `)
		ORDER BY cpsa.product_id, cpsa.position`

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("super attributes query failed: %w", err)
	}
	defer rows.Close()

	result := make(map[int][]*SuperAttributeData)
	for rows.Next() {
		sa := &SuperAttributeData{}
		if err := rows.Scan(&sa.ProductSuperAttributeID, &sa.ProductID, &sa.AttributeID,
			&sa.AttributeCode, &sa.Position, &sa.Label, &sa.UseDefault); err != nil {
			return nil, fmt.Errorf("super attributes scan failed: %w", err)
		}
		result[sa.ProductID] = append(result[sa.ProductID], sa)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("super attributes rows iteration failed: %w", err)
	}
	return result, nil
}

// GetSuperLinksForProducts batch-loads parent→child links for configurable products.
// Returns map[parentEntityID][]childEntityID.
func (r *ConfigurableRepository) GetSuperLinksForProducts(ctx context.Context, parentEntityIDs []int) (map[int][]int, error) {
	if len(parentEntityIDs) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(parentEntityIDs))
	args := make([]interface{}, len(parentEntityIDs))
	for i, id := range parentEntityIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	query := `SELECT parent_id, product_id FROM catalog_product_super_link
		WHERE parent_id IN (` + strings.Join(placeholders, ",") + `)
		ORDER BY parent_id, position`

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("super links query failed: %w", err)
	}
	defer rows.Close()

	result := make(map[int][]int)
	for rows.Next() {
		var parentID, childID int
		if err := rows.Scan(&parentID, &childID); err != nil {
			return nil, fmt.Errorf("super links scan failed: %w", err)
		}
		result[parentID] = append(result[parentID], childID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("super links rows iteration failed: %w", err)
	}
	return result, nil
}

// GetAttributeOptionLabels batch-loads option labels for given attribute_id + option_ids.
// Returns map[optionID]label.
func (r *ConfigurableRepository) GetAttributeOptionLabels(ctx context.Context, optionIDs []int, storeID int) (map[int]string, error) {
	if len(optionIDs) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(optionIDs))
	args := make([]interface{}, len(optionIDs))
	for i, id := range optionIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	var query string
	if storeID > 0 {
		query = fmt.Sprintf(`SELECT eaov_d.option_id, COALESCE(eaov_s.value, eaov_d.value) as label
			FROM eav_attribute_option_value eaov_d
			LEFT JOIN eav_attribute_option_value eaov_s ON eaov_d.option_id = eaov_s.option_id AND eaov_s.store_id = %d
			WHERE eaov_d.store_id = 0
			AND eaov_d.option_id IN (`+strings.Join(placeholders, ",")+`)`, storeID)
	} else {
		query = `SELECT option_id, value FROM eav_attribute_option_value
			WHERE store_id = 0
			AND option_id IN (` + strings.Join(placeholders, ",") + `)`
	}

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("option labels query failed: %w", err)
	}
	defer rows.Close()

	result := make(map[int]string)
	for rows.Next() {
		var optionID int
		var label string
		if err := rows.Scan(&optionID, &label); err != nil {
			return nil, fmt.Errorf("option labels scan failed: %w", err)
		}
		result[optionID] = label
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("option labels rows iteration failed: %w", err)
	}
	return result, nil
}

// GetSwatchesForOptions batch-loads swatch data for given option IDs.
// Returns map[optionID]*SwatchData.
func (r *ConfigurableRepository) GetSwatchesForOptions(ctx context.Context, optionIDs []int, storeID int) (map[int]*SwatchData, error) {
	if len(optionIDs) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(optionIDs))
	args := make([]interface{}, len(optionIDs))
	for i, id := range optionIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	// Store-scoped with fallback
	var query string
	if storeID > 0 {
		query = fmt.Sprintf(`SELECT COALESCE(s.option_id, d.option_id) as option_id,
			COALESCE(s.type, d.type) as type,
			COALESCE(s.value, d.value) as value
			FROM eav_attribute_option_swatch d
			LEFT JOIN eav_attribute_option_swatch s ON d.option_id = s.option_id AND s.store_id = %d
			WHERE d.store_id = 0
			AND d.option_id IN (`+strings.Join(placeholders, ",")+`)`, storeID)
	} else {
		query = `SELECT option_id, type, value FROM eav_attribute_option_swatch
			WHERE store_id = 0
			AND option_id IN (` + strings.Join(placeholders, ",") + `)`
	}

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("swatches query failed: %w", err)
	}
	defer rows.Close()

	result := make(map[int]*SwatchData)
	for rows.Next() {
		sw := &SwatchData{}
		var value sql.NullString
		if err := rows.Scan(&sw.OptionID, &sw.SwatchType, &value); err != nil {
			return nil, fmt.Errorf("swatches scan failed: %w", err)
		}
		if value.Valid {
			sw.Value = value.String
		}
		result[sw.OptionID] = sw
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("swatches rows iteration failed: %w", err)
	}
	return result, nil
}

// GetChildAttributeValues batch-loads the super attribute int values for child products.
// For each child, returns the option_id they have for each configurable attribute.
// Returns map[childEntityID]map[attributeID]optionID.
func (r *ConfigurableRepository) GetChildAttributeValues(ctx context.Context, childEntityIDs []int, attributeIDs []int) (map[int]map[int]int, error) {
	if len(childEntityIDs) == 0 || len(attributeIDs) == 0 {
		return nil, nil
	}

	childPlaceholders := make([]string, len(childEntityIDs))
	args := make([]interface{}, 0, len(childEntityIDs)+len(attributeIDs))
	for i, id := range childEntityIDs {
		childPlaceholders[i] = "?"
		args = append(args, id)
	}
	attrPlaceholders := make([]string, len(attributeIDs))
	for i, id := range attributeIDs {
		attrPlaceholders[i] = "?"
		args = append(args, id)
	}

	query := `SELECT cpe.entity_id, cpei.attribute_id, cpei.value
		FROM catalog_product_entity_int cpei
		JOIN catalog_product_entity cpe ON cpei.row_id = cpe.row_id
		WHERE cpe.entity_id IN (` + strings.Join(childPlaceholders, ",") + `)
		AND cpei.attribute_id IN (` + strings.Join(attrPlaceholders, ",") + `)
		AND cpei.store_id = 0`

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("child attribute values query failed: %w", err)
	}
	defer rows.Close()

	result := make(map[int]map[int]int)
	for rows.Next() {
		var entityID, attrID, value int
		if err := rows.Scan(&entityID, &attrID, &value); err != nil {
			return nil, fmt.Errorf("child attribute values scan failed: %w", err)
		}
		if result[entityID] == nil {
			result[entityID] = make(map[int]int)
		}
		result[entityID][attrID] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("child attribute values rows iteration failed: %w", err)
	}
	return result, nil
}

// GetChildProductsEAV loads EAV data for child products using the same pattern as FindProducts
// but without filters/pagination — just a simple batch load by entity_id.
func (r *ConfigurableRepository) GetChildProductsEAV(ctx context.Context, childEntityIDs []int, storeID int) ([]*ProductEAVValues, error) {
	if len(childEntityIDs) == 0 {
		return nil, nil
	}

	// Build SELECT columns
	selectCols := []string{
		"cpe.entity_id", "cpe.row_id", "cpe.sku", "cpe.type_id", "cpe.attribute_set_id",
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
			"LEFT JOIN %s %s ON cpe.row_id = %s.row_id AND %s.attribute_id = %d AND %s.store_id = 0\n",
			table, defaultAlias, defaultAlias, defaultAlias, meta.AttributeID, defaultAlias,
		)

		if storeID > 0 {
			storeAlias := attr.alias + "_s"
			fmt.Fprintf(&joinBuilder,
				"LEFT JOIN %s %s ON cpe.row_id = %s.row_id AND %s.attribute_id = %d AND %s.store_id = %d\n",
				table, storeAlias, storeAlias, storeAlias, meta.AttributeID, storeAlias, storeID,
			)
			selectCols = append(selectCols, fmt.Sprintf("COALESCE(%s.value, %s.value) as %s", storeAlias, defaultAlias, attr.alias))
		} else {
			selectCols = append(selectCols, fmt.Sprintf("%s.value as %s", defaultAlias, attr.alias))
		}
	}

	placeholders := make([]string, len(childEntityIDs))
	args := make([]interface{}, len(childEntityIDs))
	for i, id := range childEntityIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	query := "SELECT " + strings.Join(selectCols, ", ") + "\n" + joinBuilder.String() +
		"WHERE cpe.entity_id IN (" + strings.Join(placeholders, ",") + ")"

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("child products EAV query failed: %w", err)
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
			return nil, fmt.Errorf("child products scan failed: %w", err)
		}
		products = append(products, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("child products EAV rows iteration failed: %w", err)
	}
	return products, nil
}
