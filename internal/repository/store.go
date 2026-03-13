package repository

import (
	"context"
	"database/sql"
	"strings"
	"sync"
)

// StoreConfig holds cached store configuration values.
type StoreConfig struct {
	WebsiteID            int
	BaseCurrency         string
	ProductURLSuffix     string
	CategoryURLSuffix    string
	MediaBaseURL         string  // e.g. "https://example.com/media/catalog/product"
	StockThresholdQty    float64 // cataloginventory/options/stock_threshold_qty; 0 = disabled
	ProductCanonicalTag  bool    // catalog/seo/product_canonical_tag; false = disabled
}

// StoreConfigRepository caches per-store configuration.
type StoreConfigRepository struct {
	db    *sql.DB
	cache map[int]*StoreConfig
	mu    sync.RWMutex
}

func NewStoreConfigRepository(db *sql.DB) *StoreConfigRepository {
	return &StoreConfigRepository{
		db:    db,
		cache: make(map[int]*StoreConfig),
	}
}

// Get returns the cached store config, loading it on first access.
func (r *StoreConfigRepository) Get(ctx context.Context, storeID int) *StoreConfig {
	r.mu.RLock()
	if cfg, ok := r.cache[storeID]; ok {
		r.mu.RUnlock()
		return cfg
	}
	r.mu.RUnlock()

	urlRepo := NewURLRepository(r.db)
	cfg := &StoreConfig{
		WebsiteID:         urlRepo.GetWebsiteIDForStore(ctx, storeID),
		BaseCurrency:      urlRepo.GetBaseCurrency(ctx),
		ProductURLSuffix:  urlRepo.GetProductURLSuffix(ctx, storeID),
		CategoryURLSuffix: urlRepo.GetCategoryURLSuffix(ctx, storeID),
		MediaBaseURL:      r.getMediaBaseURL(ctx, storeID),
		StockThresholdQty:   r.getFloatConfig(ctx, "cataloginventory/options/stock_threshold_qty", storeID),
		ProductCanonicalTag: r.getFloatConfig(ctx, "catalog/seo/product_canonical_tag", storeID) == 1,
	}

	r.mu.Lock()
	r.cache[storeID] = cfg
	r.mu.Unlock()

	return cfg
}

// getMediaBaseURL builds the product media base URL from core_config_data.
// Checks web/secure/base_media_url first; if NULL, uses web/secure/base_url + "media/".
// Always appends "catalog/product".
func (r *StoreConfigRepository) getMediaBaseURL(ctx context.Context, storeID int) string {
	var mediaURL sql.NullString
	// Try explicit media URL first (store-scoped, then default)
	_ = r.db.QueryRowContext(ctx,
		"SELECT value FROM core_config_data WHERE path = 'web/secure/base_media_url' AND scope_id = ? AND scope = 'stores'", storeID,
	).Scan(&mediaURL)
	if !mediaURL.Valid {
		_ = r.db.QueryRowContext(ctx,
			"SELECT value FROM core_config_data WHERE path = 'web/secure/base_media_url' AND scope = 'default'",
		).Scan(&mediaURL)
	}

	base := ""
	if mediaURL.Valid && mediaURL.String != "" {
		base = strings.TrimRight(mediaURL.String, "/")
	} else {
		// Fall back to base_url + /media
		var baseURL string
		err := r.db.QueryRowContext(ctx,
			"SELECT value FROM core_config_data WHERE path = 'web/secure/base_url' AND scope_id = ? AND scope = 'stores'", storeID,
		).Scan(&baseURL)
		if err != nil {
			_ = r.db.QueryRowContext(ctx,
				"SELECT value FROM core_config_data WHERE path = 'web/secure/base_url' AND scope = 'default'",
			).Scan(&baseURL)
		}
		if baseURL == "" {
			baseURL = "http://localhost/"
		}
		base = strings.TrimRight(baseURL, "/") + "/media"
	}

	return base + "/catalog/product"
}

// getFloatConfig reads a float config value from core_config_data, returning 0 if not found.
func (r *StoreConfigRepository) getFloatConfig(ctx context.Context, path string, storeID int) float64 {
	var val float64
	// Try store-scoped, then default
	err := r.db.QueryRowContext(ctx, "SELECT value FROM core_config_data WHERE path = ? AND scope_id = ? AND scope = 'stores'", path, storeID).Scan(&val)
	if err == nil {
		return val
	}
	err = r.db.QueryRowContext(ctx, "SELECT value FROM core_config_data WHERE path = ? AND scope = 'default' AND scope_id = 0", path).Scan(&val)
	if err == nil {
		return val
	}
	return 0
}
