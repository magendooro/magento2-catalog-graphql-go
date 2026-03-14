package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// ProductLinkRepository handles related/upsell/crosssell product links.
type ProductLinkRepository struct {
	db *sql.DB
}

func NewProductLinkRepository(db *sql.DB) *ProductLinkRepository {
	return &ProductLinkRepository{db: db}
}

const (
	LinkTypeRelated   = 1
	LinkTypeUpsell    = 4
	LinkTypeCrosssell = 5
)

// ProductLinkData holds a product link relationship.
type ProductLinkData struct {
	ProductID       int // source product entity_id
	LinkedProductID int // target product entity_id
	LinkTypeID      int
}

// GetLinksForProducts batch-loads product links of the given type for the given products.
// Returns map[productEntityID][]linkedEntityID.
func (r *ProductLinkRepository) GetLinksForProducts(ctx context.Context, productEntityIDs []int, linkTypeID int) (map[int][]int, error) {
	if len(productEntityIDs) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(productEntityIDs))
	args := make([]interface{}, len(productEntityIDs)+1)
	for i, id := range productEntityIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	args[len(productEntityIDs)] = linkTypeID

	query := `SELECT product_id, linked_product_id
		FROM catalog_product_link
		WHERE product_id IN (` + strings.Join(placeholders, ",") + `)
		AND link_type_id = ?
		ORDER BY product_id, link_id`

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("product links query failed: %w", err)
	}
	defer rows.Close()

	result := make(map[int][]int)
	for rows.Next() {
		var productID, linkedID int
		if err := rows.Scan(&productID, &linkedID); err != nil {
			return nil, fmt.Errorf("product links scan failed: %w", err)
		}
		result[productID] = append(result[productID], linkedID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("product links rows iteration failed: %w", err)
	}
	return result, nil
}

// GetAllLinksForProducts batch-loads all link types (related, upsell, crosssell) at once.
// Returns three maps: related, upsell, crosssell (each map[productEntityID][]linkedEntityID).
func (r *ProductLinkRepository) GetAllLinksForProducts(ctx context.Context, productEntityIDs []int) (related, upsell, crosssell map[int][]int, err error) {
	if len(productEntityIDs) == 0 {
		return nil, nil, nil, nil
	}

	placeholders := make([]string, len(productEntityIDs))
	args := make([]interface{}, len(productEntityIDs))
	for i, id := range productEntityIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	query := `SELECT product_id, linked_product_id, link_type_id
		FROM catalog_product_link
		WHERE product_id IN (` + strings.Join(placeholders, ",") + `)
		AND link_type_id IN (1, 4, 5)
		ORDER BY product_id, link_type_id, link_id`

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("product links query failed: %w", err)
	}
	defer rows.Close()

	related = make(map[int][]int)
	upsell = make(map[int][]int)
	crosssell = make(map[int][]int)

	for rows.Next() {
		var productID, linkedID, linkType int
		if err := rows.Scan(&productID, &linkedID, &linkType); err != nil {
			return nil, nil, nil, fmt.Errorf("product links scan failed: %w", err)
		}
		switch linkType {
		case LinkTypeRelated:
			related[productID] = append(related[productID], linkedID)
		case LinkTypeUpsell:
			upsell[productID] = append(upsell[productID], linkedID)
		case LinkTypeCrosssell:
			crosssell[productID] = append(crosssell[productID], linkedID)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, nil, fmt.Errorf("product links rows iteration failed: %w", err)
	}
	return related, upsell, crosssell, nil
}
