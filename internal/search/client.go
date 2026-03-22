// Package search provides an OpenSearch/Elasticsearch client for product full-text search.
// Configuration is read from Magento's core_config_data automatically.
package search

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/rs/zerolog/log"
)

// Client connects to OpenSearch/Elasticsearch for product search.
type Client struct {
	baseURL     string
	indexAlias  string
	httpClient  *http.Client
	available   bool
}

// Config holds search engine connection settings.
type Config struct {
	Engine      string // "opensearch", "elasticsearch7", or "mysql"
	Host        string
	Port        string
	IndexPrefix string
	StoreID     int
}

// NewClient creates a search client from Magento's core_config_data.
// Returns nil if the search engine is not configured or unavailable.
func NewClient(db *sql.DB) *Client {
	cfg := loadConfig(db)
	if cfg.Engine == "mysql" || cfg.Engine == "" {
		log.Info().Msg("search engine: MySQL (no OpenSearch/Elasticsearch configured)")
		return nil
	}

	baseURL := fmt.Sprintf("http://%s:%s", cfg.Host, cfg.Port)
	indexAlias := fmt.Sprintf("%s_product_%d", cfg.IndexPrefix, cfg.StoreID)

	client := &Client{
		baseURL:    baseURL,
		indexAlias: indexAlias,
		httpClient: &http.Client{Timeout: 5 * time.Second},
	}

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := client.ping(ctx); err != nil {
		log.Warn().Err(err).Str("url", baseURL).Msg("search engine unavailable, falling back to MySQL")
		return nil
	}

	client.available = true
	log.Info().Str("engine", cfg.Engine).Str("url", baseURL).Str("index", indexAlias).Msg("search engine connected")
	return client
}

// Available returns true if the search engine is connected.
func (c *Client) Available() bool {
	return c != nil && c.available
}

func (c *Client) ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL, nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("search engine returned status %d", resp.StatusCode)
	}
	return nil
}

// Search executes a search query and returns entity_ids in relevance order + total count.
func (c *Client) Search(ctx context.Context, query *Query) ([]int, int, error) {
	body, err := json.Marshal(query)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal search query: %w", err)
	}

	url := fmt.Sprintf("%s/%s/_search", c.baseURL, c.indexAlias)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("search request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("read search response: %w", err)
	}

	if resp.StatusCode != 200 {
		return nil, 0, fmt.Errorf("search returned status %d: %s", resp.StatusCode, string(respBody[:min(len(respBody), 200)]))
	}

	var result searchResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, 0, fmt.Errorf("parse search response: %w", err)
	}

	total := result.Hits.Total.Value
	ids := make([]int, 0, len(result.Hits.Hits))
	for _, hit := range result.Hits.Hits {
		ids = append(ids, hit.ID)
	}

	return ids, total, nil
}

type searchResponse struct {
	Hits struct {
		Total struct {
			Value int `json:"value"`
		} `json:"total"`
		Hits []struct {
			ID    int     `json:"_id,string"`
			Score float64 `json:"_score"`
		} `json:"hits"`
	} `json:"hits"`
}

func loadConfig(db *sql.DB) Config {
	cfg := Config{
		Engine:      "mysql",
		Host:        "localhost",
		Port:        "9200",
		IndexPrefix: "magento2",
		StoreID:     1,
	}

	var engine string
	db.QueryRow("SELECT value FROM core_config_data WHERE path = 'catalog/search/engine' AND scope = 'default'").Scan(&engine)

	switch engine {
	case "opensearch":
		cfg.Engine = "opensearch"
		db.QueryRow("SELECT value FROM core_config_data WHERE path = 'catalog/search/opensearch_server_hostname' AND scope = 'default'").Scan(&cfg.Host)
		db.QueryRow("SELECT value FROM core_config_data WHERE path = 'catalog/search/opensearch_server_port' AND scope = 'default'").Scan(&cfg.Port)
		db.QueryRow("SELECT value FROM core_config_data WHERE path = 'catalog/search/opensearch_index_prefix' AND scope = 'default'").Scan(&cfg.IndexPrefix)
	case "elasticsearch7", "elasticsearch":
		cfg.Engine = engine
		db.QueryRow("SELECT value FROM core_config_data WHERE path = 'catalog/search/elasticsearch7_server_hostname' AND scope = 'default'").Scan(&cfg.Host)
		db.QueryRow("SELECT value FROM core_config_data WHERE path = 'catalog/search/elasticsearch7_server_port' AND scope = 'default'").Scan(&cfg.Port)
		db.QueryRow("SELECT value FROM core_config_data WHERE path = 'catalog/search/elasticsearch7_index_prefix' AND scope = 'default'").Scan(&cfg.IndexPrefix)
	}

	if cfg.IndexPrefix == "" {
		cfg.IndexPrefix = "magento2"
	}

	return cfg
}
