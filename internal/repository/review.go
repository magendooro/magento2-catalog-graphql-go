package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

// ReviewRepository handles product review queries.
type ReviewRepository struct {
	db *sql.DB
}

func NewReviewRepository(db *sql.DB) *ReviewRepository {
	return &ReviewRepository{db: db}
}

// ReviewSummaryData holds aggregated review data for a product.
type ReviewSummaryData struct {
	EntityID      int
	ReviewCount   int
	RatingSummary int // 0-100 scale
}

// ReviewData holds a single review with its ratings.
type ReviewData struct {
	ReviewID  int
	EntityID  int // product entity_id
	Title     string
	Detail    string
	Nickname  string
	CreatedAt string
	Ratings   []*ReviewRatingData
}

// ReviewRatingData holds a single rating dimension for a review.
type ReviewRatingData struct {
	RatingName string
	Value      int // 1-5 typically
	Percent    int // 0-100
}

// GetReviewSummariesForProducts batch-loads review summaries for multiple products.
func (r *ReviewRepository) GetReviewSummariesForProducts(ctx context.Context, entityIDs []int, storeID int) (map[int]*ReviewSummaryData, error) {
	if len(entityIDs) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(entityIDs))
	args := make([]interface{}, len(entityIDs))
	for i, id := range entityIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	// entity_type=1 means product reviews in Magento
	query := fmt.Sprintf(`SELECT entity_pk_value, reviews_count, rating_summary
		FROM review_entity_summary
		WHERE entity_pk_value IN (%s) AND entity_type = 1 AND store_id = %d`,
		strings.Join(placeholders, ","), storeID)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("review summaries query failed: %w", err)
	}
	defer rows.Close()

	result := make(map[int]*ReviewSummaryData)
	for rows.Next() {
		s := &ReviewSummaryData{}
		if err := rows.Scan(&s.EntityID, &s.ReviewCount, &s.RatingSummary); err != nil {
			return nil, fmt.Errorf("review summaries scan failed: %w", err)
		}
		result[s.EntityID] = s
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("review summaries rows iteration failed: %w", err)
	}
	return result, nil
}

// RatingMetadata holds metadata for a rating dimension.
type RatingMetadata struct {
	ID     string
	Name   string
	Values []*RatingValueMetadata
}

// RatingValueMetadata holds a single rating option.
type RatingValueMetadata struct {
	ID    string
	Value string
}

// RatingInput holds a rating ID and selected option ID for review creation.
type RatingInput struct {
	RatingID int
	OptionID int
}

// GetReviewsForProducts batch-loads a paginated page of approved reviews for multiple products.
// pageSize and currentPage mirror the GraphQL reviews(pageSize:, currentPage:) arguments.
// To avoid N+1 queries the batch fetches up to pageSize*currentPage rows per product and
// applies the offset in memory — efficient for typical page sizes (1–20) and pages (1–5).
func (r *ReviewRepository) GetReviewsForProducts(ctx context.Context, entityIDs []int, storeID int, pageSize, currentPage int) (map[int][]*ReviewData, error) {
	if len(entityIDs) == 0 {
		return nil, nil
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	if currentPage <= 0 {
		currentPage = 1
	}

	placeholders := make([]string, len(entityIDs))
	args := make([]interface{}, len(entityIDs))
	for i, id := range entityIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	// Load enough rows to support the requested page: up to pageSize*currentPage per product.
	// Offset is applied in Go after scanning to avoid complex per-partition SQL.
	maxPerProduct := pageSize * currentPage

	query := fmt.Sprintf(`
		SELECT r.review_id, r.entity_pk_value, rd.title, rd.detail, rd.nickname, r.created_at
		FROM review r
		JOIN review_detail rd ON r.review_id = rd.review_id
		JOIN review_store rs ON r.review_id = rs.review_id
		WHERE r.entity_pk_value IN (%s) AND r.status_id = 1 AND rs.store_id = ?
		ORDER BY r.entity_pk_value, r.created_at DESC`,
		strings.Join(placeholders, ","))

	args = append(args, storeID)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("batch reviews query failed: %w", err)
	}
	defer rows.Close()

	// allByProduct holds all fetched rows before pagination slicing.
	allByProduct := make(map[int][]*ReviewData)
	countPerProduct := make(map[int]int)

	for rows.Next() {
		rv := &ReviewData{}
		if err := rows.Scan(&rv.ReviewID, &rv.EntityID, &rv.Title, &rv.Detail, &rv.Nickname, &rv.CreatedAt); err != nil {
			return nil, fmt.Errorf("batch reviews scan failed: %w", err)
		}
		if countPerProduct[rv.EntityID] >= maxPerProduct {
			continue
		}
		countPerProduct[rv.EntityID]++
		allByProduct[rv.EntityID] = append(allByProduct[rv.EntityID], rv)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("batch reviews rows iteration failed: %w", err)
	}

	// Apply offset to produce the requested page.
	skip := (currentPage - 1) * pageSize
	result := make(map[int][]*ReviewData)
	var allReviewIDs []int
	reviewsByID := make(map[int]*ReviewData)

	for entityID, rvs := range allByProduct {
		if skip >= len(rvs) {
			result[entityID] = []*ReviewData{}
			continue
		}
		page := rvs[skip:]
		if len(page) > pageSize {
			page = page[:pageSize]
		}
		result[entityID] = page
		for _, rv := range page {
			allReviewIDs = append(allReviewIDs, rv.ReviewID)
			reviewsByID[rv.ReviewID] = rv
		}
	}

	if len(allReviewIDs) > 0 {
		ratingMap, err := r.getReviewRatings(ctx, allReviewIDs)
		if err == nil {
			for reviewID, rv := range reviewsByID {
				rv.Ratings = ratingMap[reviewID]
			}
		}
	}

	return result, nil
}

// GetRatingsMetadata loads available active ratings and their options for a store.
func (r *ReviewRepository) GetRatingsMetadata(ctx context.Context, storeID int) ([]*RatingMetadata, error) {
	// Load ratings, prefer localized name from rating_title for the store, fall back to default (store_id=0)
	query := `
		SELECT r.rating_id, COALESCE(rt_store.value, rt_default.value, r.rating_code) as name
		FROM rating r
		LEFT JOIN rating_title rt_store ON r.rating_id = rt_store.rating_id AND rt_store.store_id = ?
		LEFT JOIN rating_title rt_default ON r.rating_id = rt_default.rating_id AND rt_default.store_id = 0
		WHERE r.is_active = 1
		ORDER BY r.position, r.rating_id`

	rows, err := r.db.QueryContext(ctx, query, storeID)
	if err != nil {
		return nil, fmt.Errorf("ratings metadata query failed: %w", err)
	}
	defer rows.Close()

	var ratings []*RatingMetadata
	ratingIDs := make([]int, 0)
	ratingMap := make(map[int]*RatingMetadata)

	for rows.Next() {
		var ratingID int
		var name string
		if err := rows.Scan(&ratingID, &name); err != nil {
			return nil, fmt.Errorf("ratings metadata scan failed: %w", err)
		}
		rm := &RatingMetadata{
			ID:   strconv.Itoa(ratingID),
			Name: name,
		}
		ratings = append(ratings, rm)
		ratingIDs = append(ratingIDs, ratingID)
		ratingMap[ratingID] = rm
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ratings metadata rows iteration failed: %w", err)
	}

	if len(ratingIDs) == 0 {
		return ratings, nil
	}

	// Load options for all active ratings
	optPlaceholders := make([]string, len(ratingIDs))
	optArgs := make([]interface{}, len(ratingIDs))
	for i, id := range ratingIDs {
		optPlaceholders[i] = "?"
		optArgs[i] = id
	}

	optQuery := fmt.Sprintf(`SELECT option_id, rating_id, value FROM rating_option
		WHERE rating_id IN (%s) ORDER BY rating_id, position, option_id`,
		strings.Join(optPlaceholders, ","))

	optRows, err := r.db.QueryContext(ctx, optQuery, optArgs...)
	if err != nil {
		return nil, fmt.Errorf("rating options query failed: %w", err)
	}
	defer optRows.Close()

	for optRows.Next() {
		var optID, ratingID int
		var value int
		if err := optRows.Scan(&optID, &ratingID, &value); err != nil {
			return nil, fmt.Errorf("rating options scan failed: %w", err)
		}
		if rm, ok := ratingMap[ratingID]; ok {
			rm.Values = append(rm.Values, &RatingValueMetadata{
				ID:    strconv.Itoa(optID),
				Value: strconv.Itoa(value),
			})
		}
	}
	if err := optRows.Err(); err != nil {
		return nil, fmt.Errorf("rating options rows iteration failed: %w", err)
	}

	return ratings, nil
}

// CreateReview inserts a new product review with status pending (awaiting moderation).
// Returns the created review data.
func (r *ReviewRepository) CreateReview(ctx context.Context, entityID int, storeID int, nickname, title, detail string, ratings []RatingInput) (*ReviewData, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin transaction failed: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// entity_id=1 means product reviews in Magento
	res, err := tx.ExecContext(ctx,
		`INSERT INTO review (entity_id, entity_pk_value, status_id) VALUES (1, ?, 2)`,
		entityID)
	if err != nil {
		return nil, fmt.Errorf("insert review failed: %w", err)
	}
	reviewID64, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("get review insert ID failed: %w", err)
	}
	reviewID := int(reviewID64)

	// Insert review details
	_, err = tx.ExecContext(ctx,
		`INSERT INTO review_detail (review_id, store_id, title, detail, nickname) VALUES (?, ?, ?, ?, ?)`,
		reviewID, storeID, title, detail, nickname)
	if err != nil {
		return nil, fmt.Errorf("insert review_detail failed: %w", err)
	}

	// Associate with store
	_, err = tx.ExecContext(ctx,
		`INSERT INTO review_store (review_id, store_id) VALUES (?, ?)`,
		reviewID, storeID)
	if err != nil {
		return nil, fmt.Errorf("insert review_store failed: %w", err)
	}

	// Insert rating votes
	for _, ri := range ratings {
		// Look up percent for this option
		var percent int
		if err := tx.QueryRowContext(ctx,
			`SELECT ROUND(value * 20) FROM rating_option WHERE option_id = ? AND rating_id = ?`,
			ri.OptionID, ri.RatingID).Scan(&percent); err != nil {
			return nil, fmt.Errorf("rating option lookup failed: %w", err)
		}
		var value int
		if err := tx.QueryRowContext(ctx,
			`SELECT value FROM rating_option WHERE option_id = ?`,
			ri.OptionID).Scan(&value); err != nil {
			return nil, fmt.Errorf("rating value lookup failed: %w", err)
		}

		_, err = tx.ExecContext(ctx,
			`INSERT INTO rating_option_vote (option_id, remote_ip, remote_ip_long, entity_pk_value, rating_id, review_id, percent, value)
			 VALUES (?, '', 0, ?, ?, ?, ?, ?)`,
			ri.OptionID, entityID, ri.RatingID, reviewID, percent, value)
		if err != nil {
			return nil, fmt.Errorf("insert rating_option_vote failed: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit transaction failed: %w", err)
	}

	// Return the created review (without ratings loaded — pending reviews aren't queryable yet)
	var createdAt string
	_ = r.db.QueryRowContext(ctx, `SELECT created_at FROM review WHERE review_id = ?`, reviewID).Scan(&createdAt)

	return &ReviewData{
		ReviewID:  reviewID,
		EntityID:  entityID,
		Title:     title,
		Detail:    detail,
		Nickname:  nickname,
		CreatedAt: createdAt,
	}, nil
}

// GetReviewsForProduct loads paginated reviews for a single product.
func (r *ReviewRepository) GetReviewsForProduct(ctx context.Context, entityID int, storeID int, pageSize, currentPage int) ([]*ReviewData, int, error) {
	offset := (currentPage - 1) * pageSize

	// Count total approved reviews
	var totalCount int
	countQuery := `SELECT COUNT(*) FROM review r
		JOIN review_store rs ON r.review_id = rs.review_id
		WHERE r.entity_pk_value = ? AND r.status_id = 1 AND rs.store_id = ?`
	if err := r.db.QueryRowContext(ctx, countQuery, entityID, storeID).Scan(&totalCount); err != nil {
		return nil, 0, fmt.Errorf("review count query failed: %w", err)
	}

	if totalCount == 0 {
		return nil, 0, nil
	}

	// Load reviews with details
	query := fmt.Sprintf(`SELECT r.review_id, rd.title, rd.detail, rd.nickname, r.created_at
		FROM review r
		JOIN review_detail rd ON r.review_id = rd.review_id
		JOIN review_store rs ON r.review_id = rs.review_id
		WHERE r.entity_pk_value = ? AND r.status_id = 1 AND rs.store_id = ?
		ORDER BY r.created_at DESC
		LIMIT %d OFFSET %d`, pageSize, offset)

	rows, err := r.db.QueryContext(ctx, query, entityID, storeID)
	if err != nil {
		return nil, 0, fmt.Errorf("reviews query failed: %w", err)
	}
	defer rows.Close()

	var reviews []*ReviewData
	var reviewIDs []int
	for rows.Next() {
		rv := &ReviewData{EntityID: entityID}
		if err := rows.Scan(&rv.ReviewID, &rv.Title, &rv.Detail, &rv.Nickname, &rv.CreatedAt); err != nil {
			return nil, 0, fmt.Errorf("reviews scan failed: %w", err)
		}
		reviews = append(reviews, rv)
		reviewIDs = append(reviewIDs, rv.ReviewID)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("reviews rows iteration failed: %w", err)
	}

	if len(reviewIDs) > 0 {
		// Load ratings for these reviews
		ratingMap, err := r.getReviewRatings(ctx, reviewIDs)
		if err == nil {
			for _, rv := range reviews {
				rv.Ratings = ratingMap[rv.ReviewID]
			}
		}
	}

	return reviews, totalCount, nil
}

// getReviewRatings batch-loads rating breakdowns for multiple reviews.
func (r *ReviewRepository) getReviewRatings(ctx context.Context, reviewIDs []int) (map[int][]*ReviewRatingData, error) {
	placeholders := make([]string, len(reviewIDs))
	args := make([]interface{}, len(reviewIDs))
	for i, id := range reviewIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	query := `SELECT rov.review_id, COALESCE(rt.value, r.rating_code) as name, rov.value, rov.percent
		FROM rating_option_vote rov
		JOIN rating r ON rov.rating_id = r.rating_id
		LEFT JOIN rating_title rt ON r.rating_id = rt.rating_id AND rt.store_id = 0
		WHERE rov.review_id IN (` + strings.Join(placeholders, ",") + `)
		ORDER BY r.position`

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("review ratings query failed: %w", err)
	}
	defer rows.Close()

	result := make(map[int][]*ReviewRatingData)
	for rows.Next() {
		var reviewID int
		rd := &ReviewRatingData{}
		if err := rows.Scan(&reviewID, &rd.RatingName, &rd.Value, &rd.Percent); err != nil {
			return nil, fmt.Errorf("review ratings scan failed: %w", err)
		}
		result[reviewID] = append(result[reviewID], rd)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("review ratings rows iteration failed: %w", err)
	}
	return result, nil
}
