package repository

import (
	"strings"
	"sync"

	"github.com/magendooro/magento2-catalog-graphql-go/internal/config"
)

// StoreConfig holds cached store configuration values.
type StoreConfig struct {
	WebsiteID           int
	BaseCurrency        string
	ProductURLSuffix    string
	CategoryURLSuffix   string
	MediaBaseURL        string  // e.g. "http://localhost/media/catalog/product"
	StockThresholdQty   float64 // cataloginventory/options/stock_threshold_qty; 0 = disabled
	ProductCanonicalTag bool    // catalog/seo/product_canonical_tag; false = disabled
}

// StoreConfigRepository resolves per-store configuration using the ConfigProvider.
type StoreConfigRepository struct {
	cp    *config.ConfigProvider
	cache map[int]*StoreConfig
	mu    sync.RWMutex
}

func NewStoreConfigRepository(cp *config.ConfigProvider) *StoreConfigRepository {
	return &StoreConfigRepository{
		cp:    cp,
		cache: make(map[int]*StoreConfig),
	}
}

// Get returns the cached store config, building it on first access.
func (r *StoreConfigRepository) Get(storeID int) *StoreConfig {
	r.mu.RLock()
	if cfg, ok := r.cache[storeID]; ok {
		r.mu.RUnlock()
		return cfg
	}
	r.mu.RUnlock()

	websiteID := r.cp.GetWebsiteID(storeID)

	// Build media base URL
	mediaBaseURL := r.cp.Get("web/secure/base_media_url", storeID)
	if mediaBaseURL != "" {
		mediaBaseURL = strings.TrimRight(mediaBaseURL, "/")
	} else {
		baseURL := r.cp.Get("web/secure/base_url", storeID)
		if baseURL == "" {
			baseURL = "http://localhost/"
		}
		mediaBaseURL = strings.TrimRight(baseURL, "/") + "/media"
	}
	mediaBaseURL += "/catalog/product"

	cfg := &StoreConfig{
		WebsiteID:           websiteID,
		BaseCurrency:        or(r.cp.Get("currency/options/base", storeID), "USD"),
		ProductURLSuffix:    or(r.cp.Get("catalog/seo/product_url_suffix", storeID), ".html"),
		CategoryURLSuffix:   or(r.cp.Get("catalog/seo/category_url_suffix", storeID), ".html"),
		MediaBaseURL:        mediaBaseURL,
		StockThresholdQty:   r.cp.GetFloat("cataloginventory/options/stock_threshold_qty", storeID, 0),
		ProductCanonicalTag: r.cp.GetBool("catalog/seo/product_canonical_tag", storeID),
	}

	r.mu.Lock()
	r.cache[storeID] = cfg
	r.mu.Unlock()

	return cfg
}

func or(val, fallback string) string {
	if val != "" {
		return val
	}
	return fallback
}
