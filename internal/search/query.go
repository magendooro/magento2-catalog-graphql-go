package search

// Query represents an OpenSearch/Elasticsearch search query.
type Query struct {
	Query        boolWrapper              `json:"query"`
	From         int                      `json:"from"`
	Size         int                      `json:"size"`
	Sort         []map[string]interface{} `json:"sort,omitempty"`
	Source       bool                     `json:"_source"`
}

type boolWrapper struct {
	Bool boolQuery `json:"bool"`
}

type boolQuery struct {
	Must             []interface{} `json:"must,omitempty"`
	Should           []interface{} `json:"should,omitempty"`
	Filter           []interface{} `json:"filter,omitempty"`
	MinimumShouldMatch int         `json:"minimum_should_match,omitempty"`
}

// ProductSearchQuery builds a Magento-compatible product search query.
// Replicates the query structure from Magento's search_request.xml:
//   - match on _search field (full-text across all searchable attributes)
//   - match on sku field
//   - match_phrase_prefix on name (for partial/autocomplete matching)
//   - filter on status=1 and visibility IN (2,3,4)
func ProductSearchQuery(searchTerm string, from, size int) *Query {
	q := &Query{
		From:   from,
		Size:   size,
		Source: false, // Only need _id and _score from hits
	}

	// Should clauses: match _search, match sku, match_phrase_prefix name
	q.Query.Bool.Should = []interface{}{
		map[string]interface{}{
			"match": map[string]interface{}{
				"_search": map[string]interface{}{
					"query": searchTerm,
				},
			},
		},
		map[string]interface{}{
			"match": map[string]interface{}{
				"sku": map[string]interface{}{
					"query": searchTerm,
					"boost": 2,
				},
			},
		},
		map[string]interface{}{
			"match_phrase_prefix": map[string]interface{}{
				"name": map[string]interface{}{
					"query": searchTerm,
				},
			},
		},
	}
	q.Query.Bool.MinimumShouldMatch = 1

	// Filter: status=1, visibility in (2,3,4)
	q.Query.Bool.Filter = []interface{}{
		map[string]interface{}{"term": map[string]interface{}{"status": 1}},
		map[string]interface{}{"terms": map[string]interface{}{"visibility": []int{2, 3, 4}}},
	}

	// Default sort: relevance DESC, entity_id DESC
	q.Sort = []map[string]interface{}{
		{"_score": "desc"},
		{"_id": "desc"},
	}

	return q
}

// AddCategoryFilter adds a category_ids term filter.
func (q *Query) AddCategoryFilter(categoryID int) {
	q.Query.Bool.Filter = append(q.Query.Bool.Filter,
		map[string]interface{}{"term": map[string]interface{}{"category_ids": categoryID}},
	)
}

// AddPriceFilter adds a price range filter.
func (q *Query) AddPriceFilter(from, to *string) {
	rangeFilter := map[string]interface{}{}
	if from != nil {
		rangeFilter["gte"] = *from
	}
	if to != nil {
		rangeFilter["lte"] = *to
	}
	if len(rangeFilter) > 0 {
		q.Query.Bool.Filter = append(q.Query.Bool.Filter,
			map[string]interface{}{"range": map[string]interface{}{"price": rangeFilter}},
		)
	}
}

// SetSort overrides the default relevance sort.
func (q *Query) SetSort(field, direction string) {
	switch field {
	case "name":
		q.Sort = []map[string]interface{}{
			{field + ".sort_name": direction},
			{"_id": "desc"},
		}
	case "price":
		q.Sort = []map[string]interface{}{
			{field: direction},
			{"_id": "desc"},
		}
	case "position":
		// Position is category-specific, keep relevance
	}
}
