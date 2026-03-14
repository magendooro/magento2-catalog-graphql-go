package repository

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/magendooro/magento2-catalog-graphql-go/graph/model"
)

type InventoryRepository struct {
	db *sql.DB
}

func NewInventoryRepository(db *sql.DB) *InventoryRepository {
	return &InventoryRepository{db: db}
}

// InventoryData holds stock data for a product.
type InventoryData struct {
	ProductID   int
	StockStatus int // 1=in stock, 0=out of stock
	Qty         float64
	MinSaleQty  float64
	MaxSaleQty  float64
}

// GetInventoryForProducts batch-loads inventory data.
func (r *InventoryRepository) GetInventoryForProducts(ctx context.Context, entityIDs []int) (map[int]*InventoryData, error) {
	if len(entityIDs) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(entityIDs))
	args := make([]interface{}, len(entityIDs))
	for i, id := range entityIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	query := fmt.Sprintf(`
		SELECT
			si.product_id,
			si.is_in_stock,
			si.qty,
			si.min_sale_qty,
			si.max_sale_qty
		FROM cataloginventory_stock_item si
		WHERE si.product_id IN (%s)
	`, joinPlaceholders(placeholders))

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("inventory query failed: %w", err)
	}
	defer rows.Close()

	result := make(map[int]*InventoryData)
	for rows.Next() {
		inv := &InventoryData{}
		if err := rows.Scan(&inv.ProductID, &inv.StockStatus, &inv.Qty, &inv.MinSaleQty, &inv.MaxSaleQty); err != nil {
			return nil, fmt.Errorf("inventory scan failed: %w", err)
		}
		result[inv.ProductID] = inv
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("inventory rows iteration failed: %w", err)
	}
	return result, nil
}

// BuildStockStatus converts InventoryData to GraphQL stock status.
func BuildStockStatus(inv *InventoryData) *model.ProductStockStatus {
	if inv == nil {
		return nil
	}
	if inv.StockStatus == 1 {
		s := model.ProductStockStatusInStock
		return &s
	}
	s := model.ProductStockStatusOutOfStock
	return &s
}
