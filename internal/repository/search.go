package repository

import (
	"context"
	"database/sql"
	"fmt"
)

// SearchRepository handles search suggestions from Magento's search_query table.
type SearchRepository struct {
	db *sql.DB
}

func NewSearchRepository(db *sql.DB) *SearchRepository {
	return &SearchRepository{db: db}
}

// SearchSuggestionData holds a search suggestion.
type SearchSuggestionData struct {
	QueryText string
}

// GetSearchSuggestions returns search suggestions matching the given search term.
func (r *SearchRepository) GetSearchSuggestions(ctx context.Context, search string, storeID int, limit int) ([]*SearchSuggestionData, error) {
	query := `SELECT query_text FROM search_query
		WHERE query_text LIKE ? AND store_id = ? AND is_active = 1 AND num_results > 0
		ORDER BY popularity DESC
		LIMIT ?`

	rows, err := r.db.QueryContext(ctx, query, "%"+search+"%", storeID, limit)
	if err != nil {
		return nil, fmt.Errorf("search suggestions query failed: %w", err)
	}
	defer rows.Close()

	var suggestions []*SearchSuggestionData
	for rows.Next() {
		s := &SearchSuggestionData{}
		if err := rows.Scan(&s.QueryText); err != nil {
			return nil, fmt.Errorf("search suggestions scan failed: %w", err)
		}
		suggestions = append(suggestions, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search suggestions rows iteration failed: %w", err)
	}
	return suggestions, nil
}
