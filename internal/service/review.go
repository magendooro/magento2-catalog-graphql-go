package service

import (
	"context"
	"fmt"
	"strconv"

	"github.com/magendooro/magento2-go-common/middleware"
	"github.com/magendooro/magento2-catalog-graphql-go/graph/model"
	"github.com/magendooro/magento2-catalog-graphql-go/internal/repository"
)

// ReviewService handles product review queries and mutations.
type ReviewService struct {
	reviewRepo  *repository.ReviewRepository
	productRepo *repository.ProductRepository
}

func NewReviewService(reviewRepo *repository.ReviewRepository, productRepo *repository.ProductRepository) *ReviewService {
	return &ReviewService{
		reviewRepo:  reviewRepo,
		productRepo: productRepo,
	}
}

// GetRatingsMetadata returns all active rating dimensions with their options.
func (s *ReviewService) GetRatingsMetadata(ctx context.Context) (*model.ProductReviewRatingsMetadata, error) {
	storeID := middleware.GetStoreID(ctx)
	ratings, err := s.reviewRepo.GetRatingsMetadata(ctx, storeID)
	if err != nil {
		return nil, fmt.Errorf("failed to load ratings metadata: %w", err)
	}

	items := make([]*model.ProductReviewRatingMetadata, 0, len(ratings))
	for _, r := range ratings {
		values := make([]*model.ProductReviewRatingValueMetadata, 0, len(r.Values))
		for _, v := range r.Values {
			values = append(values, &model.ProductReviewRatingValueMetadata{
				Value:   v.Value,
				ValueID: v.ID,
			})
		}
		items = append(items, &model.ProductReviewRatingMetadata{
			ID:     r.ID,
			Name:   r.Name,
			Values: values,
		})
	}

	return &model.ProductReviewRatingsMetadata{Items: items}, nil
}

// CreateProductReview submits a new product review (pending moderation).
func (s *ReviewService) CreateProductReview(ctx context.Context, input *model.CreateProductReviewInput) (*model.CreateProductReviewOutput, error) {
	storeID := middleware.GetStoreID(ctx)

	// Resolve SKU to entity ID
	entityID, err := s.productRepo.GetEntityIDBySKU(ctx, input.Sku)
	if err != nil {
		return nil, fmt.Errorf("product not found: %w", err)
	}

	// Parse rating inputs
	ratingInputs := make([]repository.RatingInput, 0, len(input.Ratings))
	for _, ri := range input.Ratings {
		ratingID, err := strconv.Atoi(ri.ID)
		if err != nil {
			return nil, fmt.Errorf("invalid rating id %q: %w", ri.ID, err)
		}
		valueID, err := strconv.Atoi(ri.ValueID)
		if err != nil {
			return nil, fmt.Errorf("invalid rating value_id %q: %w", ri.ValueID, err)
		}
		ratingInputs = append(ratingInputs, repository.RatingInput{
			RatingID: ratingID,
			OptionID: valueID,
		})
	}

	rv, err := s.reviewRepo.CreateReview(ctx, entityID, storeID, input.Nickname, input.Summary, input.Text, ratingInputs)
	if err != nil {
		return nil, fmt.Errorf("failed to create review: %w", err)
	}

	return &model.CreateProductReviewOutput{
		Review: &model.ProductReview{
			Summary:          rv.Title,
			Text:             rv.Detail,
			Nickname:         rv.Nickname,
			CreatedAt:        rv.CreatedAt,
			AverageRating:    0, // pending — no votes counted yet
			RatingsBreakdown: []*model.ProductReviewRating{},
		},
	}, nil
}
