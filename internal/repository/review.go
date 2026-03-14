package repository

import (
	"context"
	"database/sql"
	"fmt"
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
