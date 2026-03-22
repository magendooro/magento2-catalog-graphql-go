package repository

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"strings"
)

// BundleRepository handles bundle product data (options, selections).
type BundleRepository struct {
	db       *sql.DB
	attrRepo *AttributeRepository
}

func NewBundleRepository(db *sql.DB, attrRepo *AttributeRepository) *BundleRepository {
	return &BundleRepository{db: db, attrRepo: attrRepo}
}

// BundleOptionData holds a bundle option definition.
type BundleOptionData struct {
	OptionID int
	ParentID int // parent entity_id
	Required bool
	Position int
	Type     string // checkbox, select, radio, multi
	Title    string
}

// BundleSelectionData holds a bundle selection (product within an option).
type BundleSelectionData struct {
	SelectionID       int
	OptionID          int
	ParentProductID   int
	ProductID         int // child entity_id
	Position          int
	IsDefault         bool
	SelectionPriceType int // 0=fixed, 1=percent
	SelectionPrice    float64
	SelectionQty      float64
	CanChangeQty      bool
}

// BundleAttributeData holds bundle-specific EAV int attributes.
type BundleAttributeData struct {
	DynamicPrice  bool // price_type: 0=dynamic, 1=fixed
	DynamicSku    bool // sku_type: 0=dynamic, 1=fixed
	DynamicWeight bool // weight_type: 0=dynamic, 1=fixed
	PriceView     int  // 0=price range, 1=as low as
	ShipmentType  int  // 0=together, 1=separately
}

// GetBundleOptionsForProducts batch-loads bundle options for given parent entity IDs.
// Returns map[parentEntityID][]*BundleOptionData.
func (r *BundleRepository) GetBundleOptionsForProducts(ctx context.Context, parentEntityIDs []int, storeID int) (map[int][]*BundleOptionData, error) {
	if len(parentEntityIDs) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(parentEntityIDs))
	args := make([]interface{}, len(parentEntityIDs))
	for i, id := range parentEntityIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	var query string
	if storeID > 0 {
		query = fmt.Sprintf(`SELECT bo.option_id, bo.parent_id, bo.required, bo.position, bo.type,
			COALESCE(bov_s.title, bov_d.title) as title
			FROM catalog_product_bundle_option bo
			LEFT JOIN catalog_product_bundle_option_value bov_d ON bo.option_id = bov_d.option_id AND bov_d.store_id = 0
			LEFT JOIN catalog_product_bundle_option_value bov_s ON bo.option_id = bov_s.option_id AND bov_s.store_id = %d
			WHERE bo.parent_id IN (`+strings.Join(placeholders, ",")+`)
			ORDER BY bo.parent_id, bo.position`, storeID)
	} else {
		query = `SELECT bo.option_id, bo.parent_id, bo.required, bo.position, bo.type,
			COALESCE(bov_d.title, '') as title
			FROM catalog_product_bundle_option bo
			LEFT JOIN catalog_product_bundle_option_value bov_d ON bo.option_id = bov_d.option_id AND bov_d.store_id = 0
			WHERE bo.parent_id IN (` + strings.Join(placeholders, ",") + `)
			ORDER BY bo.parent_id, bo.position`
	}

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("bundle options query failed: %w", err)
	}
	defer rows.Close()

	result := make(map[int][]*BundleOptionData)
	for rows.Next() {
		bo := &BundleOptionData{}
		if err := rows.Scan(&bo.OptionID, &bo.ParentID, &bo.Required, &bo.Position, &bo.Type, &bo.Title); err != nil {
			return nil, fmt.Errorf("bundle options scan failed: %w", err)
		}
		result[bo.ParentID] = append(result[bo.ParentID], bo)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("bundle options rows iteration failed: %w", err)
	}
	return result, nil
}

// GetBundleSelectionsForOptions batch-loads bundle selections for given option IDs.
// Returns map[optionID][]*BundleSelectionData.
func (r *BundleRepository) GetBundleSelectionsForOptions(ctx context.Context, optionIDs []int) (map[int][]*BundleSelectionData, error) {
	if len(optionIDs) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(optionIDs))
	args := make([]interface{}, len(optionIDs))
	for i, id := range optionIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	query := `SELECT selection_id, option_id, parent_product_id, product_id, position,
		is_default, selection_price_type, selection_price_value,
		COALESCE(selection_qty, 1), selection_can_change_qty
		FROM catalog_product_bundle_selection
		WHERE option_id IN (` + strings.Join(placeholders, ",") + `)
		ORDER BY option_id, position`

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("bundle selections query failed: %w", err)
	}
	defer rows.Close()

	result := make(map[int][]*BundleSelectionData)
	for rows.Next() {
		bs := &BundleSelectionData{}
		if err := rows.Scan(&bs.SelectionID, &bs.OptionID, &bs.ParentProductID, &bs.ProductID,
			&bs.Position, &bs.IsDefault, &bs.SelectionPriceType, &bs.SelectionPrice,
			&bs.SelectionQty, &bs.CanChangeQty); err != nil {
			return nil, fmt.Errorf("bundle selections scan failed: %w", err)
		}
		result[bs.OptionID] = append(result[bs.OptionID], bs)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("bundle selections rows iteration failed: %w", err)
	}
	return result, nil
}

// GetBundleAttributesForProducts batch-loads bundle-specific EAV int attributes.
// Returns map[entityID]*BundleAttributeData.
func (r *BundleRepository) GetBundleAttributesForProducts(ctx context.Context, entityIDs []int) (map[int]*BundleAttributeData, error) {
	if len(entityIDs) == 0 {
		return nil, nil
	}

	// Resolve attribute IDs dynamically
	bundleAttrs := []string{"price_type", "sku_type", "weight_type", "price_view", "shipment_type"}
	attrIDMap := make(map[int]string) // attrID → code
	var attrIDs []int
	for _, code := range bundleAttrs {
		meta := r.attrRepo.Get(code)
		if meta != nil {
			attrIDMap[meta.AttributeID] = code
			attrIDs = append(attrIDs, meta.AttributeID)
		}
	}
	if len(attrIDs) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(entityIDs))
	args := make([]interface{}, len(entityIDs))
	for i, id := range entityIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	attrPlaceholders := make([]string, len(attrIDs))
	for i, id := range attrIDs {
		attrPlaceholders[i] = "?"
		args = append(args, id)
	}

	query := `SELECT cpe.entity_id, cpei.attribute_id, cpei.value
		FROM catalog_product_entity_int cpei
		JOIN catalog_product_entity cpe ON cpei.entity_id = cpe.entity_id
		WHERE cpe.entity_id IN (` + strings.Join(placeholders, ",") + `)
		AND cpei.attribute_id IN (` + strings.Join(attrPlaceholders, ",") + `)
		AND cpei.store_id = 0`

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("bundle attributes query failed: %w", err)
	}
	defer rows.Close()

	result := make(map[int]*BundleAttributeData)
	for rows.Next() {
		var entityID, attrID, value int
		if err := rows.Scan(&entityID, &attrID, &value); err != nil {
			return nil, fmt.Errorf("bundle attributes scan failed: %w", err)
		}
		if result[entityID] == nil {
			result[entityID] = &BundleAttributeData{}
		}
		ba := result[entityID]
		switch attrIDMap[attrID] {
		case "price_type":
			ba.DynamicPrice = value == 0
		case "sku_type":
			ba.DynamicSku = value == 0
		case "weight_type":
			ba.DynamicWeight = value == 0
		case "price_view":
			ba.PriceView = value
		case "shipment_type":
			ba.ShipmentType = value
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("bundle attributes rows iteration failed: %w", err)
	}
	return result, nil
}

// EncodeBundleItemUID encodes a bundle item (option) UID: base64("bundle/{option_id}")
func EncodeBundleItemUID(optionID int) string {
	return base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("bundle/%d", optionID)))
}

// EncodeBundleOptionUID encodes a bundle option (selection) UID: base64("bundle/{option_id}/{selection_id}/{qty}")
func EncodeBundleOptionUID(optionID, selectionID int, qty int) string {
	return base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("bundle/%d/%d/%d", optionID, selectionID, qty)))
}
