package repository

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/magendooro/magento2-catalog-graphql-go/graph/model"
)

type PriceRepository struct {
	db *sql.DB
}

func NewPriceRepository(db *sql.DB) *PriceRepository {
	return &PriceRepository{db: db}
}

// PriceData holds price index data for a product.
type PriceData struct {
	EntityID   int
	Price      *float64
	FinalPrice *float64
	MinPrice   *float64
	MaxPrice   *float64
	TierPrice  *float64
}

// GetPricesForProducts batch-loads prices for a list of entity IDs.
func (r *PriceRepository) GetPricesForProducts(ctx context.Context, entityIDs []int, websiteID int) (map[int]*PriceData, error) {
	if len(entityIDs) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(entityIDs))
	args := make([]interface{}, len(entityIDs)+2)
	for i, id := range entityIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	args[len(entityIDs)] = 0   // customer_group_id (NOT LOGGED IN)
	args[len(entityIDs)+1] = websiteID

	query := fmt.Sprintf(`
		SELECT entity_id, price, final_price, min_price, max_price, tier_price
		FROM catalog_product_index_price
		WHERE entity_id IN (%s) AND customer_group_id = ? AND website_id = ?
	`, joinPlaceholders(placeholders))

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("price query failed: %w", err)
	}
	defer rows.Close()

	result := make(map[int]*PriceData)
	for rows.Next() {
		p := &PriceData{}
		if err := rows.Scan(&p.EntityID, &p.Price, &p.FinalPrice, &p.MinPrice, &p.MaxPrice, &p.TierPrice); err != nil {
			return nil, fmt.Errorf("price scan failed: %w", err)
		}
		result[p.EntityID] = p
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("price rows iteration failed: %w", err)
	}
	return result, nil
}

// TierPriceData holds tier price data for a product.
type TierPriceData struct {
	RowID           int
	AllGroups       int
	CustomerGroupID int
	Qty             float64
	Value           float64
	WebsiteID       int
	PercentageValue *float64
}

// GetTierPricesForProducts batch-loads tier prices.
func (r *PriceRepository) GetTierPricesForProducts(ctx context.Context, rowIDs []int, websiteID int) (map[int][]*TierPriceData, error) {
	if len(rowIDs) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(rowIDs))
	args := make([]interface{}, len(rowIDs)+1)
	for i, id := range rowIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	args[len(rowIDs)] = websiteID

	query := fmt.Sprintf(`
		SELECT entity_id, all_groups, customer_group_id, qty, value, website_id, percentage_value
		FROM catalog_product_entity_tier_price
		WHERE entity_id IN (%s) AND (website_id = ? OR website_id = 0)
		AND (all_groups = 1 OR customer_group_id = 0)
		ORDER BY qty ASC
	`, joinPlaceholders(placeholders))

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("tier price query failed: %w", err)
	}
	defer rows.Close()

	result := make(map[int][]*TierPriceData)
	for rows.Next() {
		tp := &TierPriceData{}
		if err := rows.Scan(&tp.RowID, &tp.AllGroups, &tp.CustomerGroupID, &tp.Qty, &tp.Value, &tp.WebsiteID, &tp.PercentageValue); err != nil {
			return nil, fmt.Errorf("tier price scan failed: %w", err)
		}
		result[tp.RowID] = append(result[tp.RowID], tp)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tier price rows iteration failed: %w", err)
	}
	return result, nil
}

// BuildPriceRange converts PriceData to the GraphQL PriceRange type.
func BuildPriceRange(pd *PriceData, currency model.CurrencyEnum) *model.PriceRange {
	zero := 0.0
	zeroDiscount := &model.ProductDiscount{AmountOff: &zero, PercentOff: &zero}

	if pd == nil {
		return &model.PriceRange{
			MinimumPrice: &model.ProductPrice{
				RegularPrice: &model.Money{},
				FinalPrice:   &model.Money{},
				Discount:     zeroDiscount,
			},
			MaximumPrice: &model.ProductPrice{
				RegularPrice: &model.Money{},
				FinalPrice:   &model.Money{},
				Discount:     zeroDiscount,
			},
		}
	}

	regularPrice := valOrZero(pd.Price)
	finalPrice := valOrZero(pd.FinalPrice)
	minPrice := valOrZero(pd.MinPrice)
	maxPrice := valOrZero(pd.MaxPrice)

	var minRegular, maxRegular, minFinal, maxFinal float64

	if regularPrice > 0 {
		// Product has its own price (simple, fixed-price bundle)
		minRegular = regularPrice
		maxRegular = regularPrice
		minFinal = finalPrice
		maxFinal = finalPrice
	} else {
		// Price derived from children (configurable, dynamic bundle)
		minRegular = minPrice
		maxRegular = maxPrice
		minFinal = minPrice
		maxFinal = maxPrice
	}

	minDiscount := computeDiscount(minRegular, minFinal)
	maxDiscount := computeDiscount(maxRegular, maxFinal)

	minProductPrice := &model.ProductPrice{
		RegularPrice: &model.Money{Value: &minRegular, Currency: &currency},
		FinalPrice:   &model.Money{Value: &minFinal, Currency: &currency},
		Discount:     minDiscount,
	}
	maxProductPrice := &model.ProductPrice{
		RegularPrice: &model.Money{Value: &maxRegular, Currency: &currency},
		FinalPrice:   &model.Money{Value: &maxFinal, Currency: &currency},
		Discount:     maxDiscount,
	}

	return &model.PriceRange{
		MinimumPrice: minProductPrice,
		MaximumPrice: maxProductPrice,
	}
}

// BuildTierPrices converts TierPriceData to GraphQL TierPrice types.
func BuildTierPrices(tiers []*TierPriceData, regularPrice float64, currency model.CurrencyEnum) []*model.TierPrice {
	if len(tiers) == 0 {
		return []*model.TierPrice{}
	}
	result := make([]*model.TierPrice, 0, len(tiers))
	for _, t := range tiers {
		fp := t.Value
		qty := t.Qty
		discount := computeDiscount(regularPrice, fp)
		result = append(result, &model.TierPrice{
			FinalPrice: &model.Money{Value: &fp, Currency: &currency},
			Quantity:   &qty,
			Discount:   discount,
		})
	}
	return result
}

func computeDiscount(regular, final float64) *model.ProductDiscount {
	zero := 0.0
	if regular <= 0 || final >= regular {
		return &model.ProductDiscount{AmountOff: &zero, PercentOff: &zero}
	}
	amountOff := regular - final
	percentOff := (amountOff / regular) * 100
	return &model.ProductDiscount{
		AmountOff:  &amountOff,
		PercentOff: &percentOff,
	}
}

func valOrZero(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

func joinPlaceholders(p []string) string {
	result := ""
	for i, s := range p {
		if i > 0 {
			result += ","
		}
		result += s
	}
	return result
}
