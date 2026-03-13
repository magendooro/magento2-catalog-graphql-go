package repository

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/magendooro/magento2-catalog-graphql-go/graph/model"
)

type MediaRepository struct {
	db *sql.DB
}

func NewMediaRepository(db *sql.DB) *MediaRepository {
	return &MediaRepository{db: db}
}

// MediaGalleryData holds a single media gallery entry.
type MediaGalleryData struct {
	ValueID   int
	RowID     int
	MediaType string
	File      string
	Label     *string
	Position  *int
	Disabled  int
}

// GetMediaForProducts batch-loads media gallery entries for products.
// Uses row_id for Magento EE. Returns map keyed by row_id.
func (r *MediaRepository) GetMediaForProducts(ctx context.Context, rowIDs []int, storeID int) (map[int][]*MediaGalleryData, error) {
	if len(rowIDs) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(rowIDs))
	args := make([]interface{}, 0, len(rowIDs)+1)
	for i, id := range rowIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	args = append(args, storeID)

	// Query with store-scoped label/position/disabled, fallback to store_id=0
	query := fmt.Sprintf(`
		SELECT
			mg.value_id,
			mgve.row_id,
			mg.media_type,
			mg.value AS file,
			COALESCE(mgv_store.label, mgv_default.label) AS label,
			COALESCE(mgv_store.position, mgv_default.position) AS position,
			COALESCE(mgv_store.disabled, mgv_default.disabled, 0) AS disabled
		FROM catalog_product_entity_media_gallery mg
		INNER JOIN catalog_product_entity_media_gallery_value_to_entity mgve
			ON mg.value_id = mgve.value_id
		LEFT JOIN catalog_product_entity_media_gallery_value mgv_default
			ON mg.value_id = mgv_default.value_id AND mgv_default.row_id = mgve.row_id AND mgv_default.store_id = 0
		LEFT JOIN catalog_product_entity_media_gallery_value mgv_store
			ON mg.value_id = mgv_store.value_id AND mgv_store.row_id = mgve.row_id AND mgv_store.store_id = ?
		WHERE mgve.row_id IN (%s)
			AND mg.disabled = 0
		ORDER BY COALESCE(mgv_store.position, mgv_default.position) ASC
	`, joinPlaceholders(placeholders))

	// Reorder args: store_id first (for the LEFT JOIN), then row_ids
	finalArgs := make([]interface{}, 0, len(args))
	finalArgs = append(finalArgs, storeID)
	for _, id := range rowIDs {
		finalArgs = append(finalArgs, id)
	}

	rows, err := r.db.QueryContext(ctx, query, finalArgs...)
	if err != nil {
		return nil, fmt.Errorf("media query failed: %w", err)
	}
	defer rows.Close()

	result := make(map[int][]*MediaGalleryData)
	seen := make(map[string]bool) // dedupe by row_id+value_id
	for rows.Next() {
		m := &MediaGalleryData{}
		if err := rows.Scan(&m.ValueID, &m.RowID, &m.MediaType, &m.File, &m.Label, &m.Position, &m.Disabled); err != nil {
			return nil, fmt.Errorf("media scan failed: %w", err)
		}
		if m.Disabled != 0 {
			continue
		}
		key := fmt.Sprintf("%d-%d", m.RowID, m.ValueID)
		if seen[key] {
			continue
		}
		seen[key] = true
		result[m.RowID] = append(result[m.RowID], m)
	}
	return result, nil
}

// BuildMediaGallery converts MediaGalleryData to GraphQL types.
// labelFallback is used when the media item has no label (Magento uses product name).
func BuildMediaGallery(items []*MediaGalleryData, baseURL string, labelFallback *string) []model.MediaGalleryInterface {
	if len(items) == 0 {
		return nil
	}
	result := make([]model.MediaGalleryInterface, 0, len(items))
	for _, m := range items {
		url := baseURL + m.File
		pos := 0
		if m.Position != nil {
			pos = *m.Position
		}
		disabled := m.Disabled != 0
		label := m.Label
		if label == nil {
			label = labelFallback
		}

		if m.MediaType == "external-video" {
			result = append(result, &model.ProductVideo{
				URL:      &url,
				Label:    label,
				Position: &pos,
				Disabled: &disabled,
			})
		} else {
			result = append(result, &model.ProductImage{
				URL:      &url,
				Label:    label,
				Position: &pos,
				Disabled: &disabled,
			})
		}
	}
	return result
}
