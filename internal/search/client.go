// Package search provides an OpenSearch/Elasticsearch client for product full-text search.
// Configuration is read from Magento's core_config_data automatically.
package search

import (
	"bytes"
	"context"
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

// NewClient creates a search client from Magento's config.
// Returns nil if the search engine is not configured or unavailable.
func NewClient(cp ConfigReader) *Client {
	cfg := loadConfigFromProvider(cp)
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

// SearchWithAggregations executes a search and returns entity_ids + aggregation buckets.
func (c *Client) SearchWithAggregations(ctx context.Context, query *Query) ([]int, int, map[string][]AggregationOption, error) {
	body, err := json.Marshal(query)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("marshal search query: %w", err)
	}

	url := fmt.Sprintf("%s/%s/_search", c.baseURL, c.indexAlias)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("search request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("read search response: %w", err)
	}

	if resp.StatusCode != 200 {
		return nil, 0, nil, fmt.Errorf("search returned %d: %s", resp.StatusCode, string(respBody[:min(len(respBody), 200)]))
	}

	var result searchResponseWithAggs
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, 0, nil, fmt.Errorf("parse search response: %w", err)
	}

	total := result.Hits.Total.Value
	ids := make([]int, 0, len(result.Hits.Hits))
	for _, hit := range result.Hits.Hits {
		ids = append(ids, hit.ID)
	}

	// Parse aggregations
	aggs := make(map[string][]AggregationOption)
	for name, raw := range result.Aggregations {
		var aggResult struct {
			Buckets []struct {
				Key      json.RawMessage `json:"key"`
				DocCount int             `json:"doc_count"`
			} `json:"buckets"`
		}
		if err := json.Unmarshal(raw, &aggResult); err != nil {
			continue
		}
		options := make([]AggregationOption, 0, len(aggResult.Buckets))
		for _, b := range aggResult.Buckets {
			key := string(b.Key)
			// Remove quotes from string keys
			if len(key) > 1 && key[0] == '"' {
				key = key[1 : len(key)-1]
			}
			options = append(options, AggregationOption{
				Key:      key,
				DocCount: b.DocCount,
			})
		}
		if len(options) > 0 {
			aggs[name] = options
		}
	}

	return ids, total, aggs, nil
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

type searchResponseWithAggs struct {
	searchResponse
	Aggregations map[string]json.RawMessage `json:"aggregations"`
}

// ConfigReader is the interface needed from the config provider.
type ConfigReader interface {
	GetDefault(path string) string
}

func loadConfigFromProvider(cp ConfigReader) Config {
	cfg := Config{
		Engine:      "mysql",
		Host:        "localhost",
		Port:        "9200",
		IndexPrefix: "magento2",
		StoreID:     1,
	}

	engine := cp.GetDefault("catalog/search/engine")

	switch engine {
	case "opensearch":
		cfg.Engine = "opensearch"
		if v := cp.GetDefault("catalog/search/opensearch_server_hostname"); v != "" {
			cfg.Host = v
		}
		if v := cp.GetDefault("catalog/search/opensearch_server_port"); v != "" {
			cfg.Port = v
		}
		if v := cp.GetDefault("catalog/search/opensearch_index_prefix"); v != "" {
			cfg.IndexPrefix = v
		}
	case "elasticsearch7", "elasticsearch":
		cfg.Engine = engine
		if v := cp.GetDefault("catalog/search/elasticsearch7_server_hostname"); v != "" {
			cfg.Host = v
		}
		if v := cp.GetDefault("catalog/search/elasticsearch7_server_port"); v != "" {
			cfg.Port = v
		}
		if v := cp.GetDefault("catalog/search/elasticsearch7_index_prefix"); v != "" {
			cfg.IndexPrefix = v
		}
	}

	return cfg
}
