package config

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"sync"

	"github.com/rs/zerolog/log"
)

// ScopeDefault is the default scope (applies to all stores/websites).
const ScopeDefault = "default"

// ScopeWebsite is the website-level scope.
const ScopeWebsite = "websites"

// ScopeStore is the store-level scope.
const ScopeStore = "stores"

// ConfigProvider reads Magento's core_config_data with proper scope hierarchy.
// All values are preloaded at startup and cached in memory.
//
// Scope resolution order (matching Magento):
//
//	store (scope='stores', scope_id=store_id)
//	  → website (scope='websites', scope_id=website_id)
//	    → default (scope='default', scope_id=0)
type ConfigProvider struct {
	db *sql.DB

	// values: scope → scope_id → path → value
	values map[string]map[int]map[string]string

	// storeToWebsite: store_id → website_id (for scope fallback)
	storeToWebsite map[int]int

	mu sync.RWMutex
}

// NewConfigProvider creates a new config provider and preloads all values.
func NewConfigProvider(db *sql.DB) (*ConfigProvider, error) {
	p := &ConfigProvider{
		db:             db,
		values:         make(map[string]map[int]map[string]string),
		storeToWebsite: make(map[int]int),
	}

	if err := p.load(context.Background()); err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	count := 0
	for _, scopeMap := range p.values {
		for _, pathMap := range scopeMap {
			count += len(pathMap)
		}
	}
	log.Info().Int("values", count).Msg("config provider loaded")
	return p, nil
}

func (p *ConfigProvider) load(ctx context.Context) error {
	// Load store → website mapping
	rows, err := p.db.QueryContext(ctx, "SELECT store_id, website_id FROM store")
	if err != nil {
		return fmt.Errorf("load store mapping: %w", err)
	}
	for rows.Next() {
		var storeID, websiteID int
		if err := rows.Scan(&storeID, &websiteID); err != nil {
			rows.Close()
			return err
		}
		p.storeToWebsite[storeID] = websiteID
	}
	rows.Close()

	// Load all config values
	rows, err = p.db.QueryContext(ctx, "SELECT scope, scope_id, path, value FROM core_config_data")
	if err != nil {
		return fmt.Errorf("load config data: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var scope string
		var scopeID int
		var path, value string
		if err := rows.Scan(&scope, &scopeID, &path, &value); err != nil {
			return err
		}

		if p.values[scope] == nil {
			p.values[scope] = make(map[int]map[string]string)
		}
		if p.values[scope][scopeID] == nil {
			p.values[scope][scopeID] = make(map[string]string)
		}
		p.values[scope][scopeID][path] = value
	}

	return rows.Err()
}

// Get returns the config value for a path, resolving scope hierarchy:
// store (scope_id=storeID) → website → default.
// Returns empty string if not found.
func (p *ConfigProvider) Get(path string, storeID int) string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	// 1. Check store scope
	if scopeMap, ok := p.values[ScopeStore]; ok {
		if pathMap, ok := scopeMap[storeID]; ok {
			if val, ok := pathMap[path]; ok {
				return val
			}
		}
	}

	// 2. Check website scope
	if websiteID, ok := p.storeToWebsite[storeID]; ok {
		if scopeMap, ok := p.values[ScopeWebsite]; ok {
			if pathMap, ok := scopeMap[websiteID]; ok {
				if val, ok := pathMap[path]; ok {
					return val
				}
			}
		}
	}

	// 3. Check default scope
	if scopeMap, ok := p.values[ScopeDefault]; ok {
		if pathMap, ok := scopeMap[0]; ok {
			if val, ok := pathMap[path]; ok {
				return val
			}
		}
	}

	return ""
}

// GetDefault returns the default-scope value (shortcut when storeID is irrelevant).
func (p *ConfigProvider) GetDefault(path string) string {
	return p.Get(path, 0)
}

// GetInt returns the config value as an integer, or defaultVal if not found/invalid.
func (p *ConfigProvider) GetInt(path string, storeID int, defaultVal int) int {
	s := p.Get(path, storeID)
	if s == "" {
		return defaultVal
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return defaultVal
	}
	return v
}

// GetFloat returns the config value as a float, or defaultVal if not found/invalid.
func (p *ConfigProvider) GetFloat(path string, storeID int, defaultVal float64) float64 {
	s := p.Get(path, storeID)
	if s == "" {
		return defaultVal
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return defaultVal
	}
	return v
}

// GetBool returns the config value as a boolean. "1" and "true" are true, everything else is false.
func (p *ConfigProvider) GetBool(path string, storeID int) bool {
	s := p.Get(path, storeID)
	return s == "1" || s == "true"
}

// GetWebsiteID returns the website_id for a given store_id.
func (p *ConfigProvider) GetWebsiteID(storeID int) int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if wid, ok := p.storeToWebsite[storeID]; ok {
		return wid
	}
	return 1 // default website
}
