package service

import (
	"context"
	"encoding/base64"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/magendooro/magento2-catalog-graphql-go/graph/model"
	"github.com/magendooro/magento2-catalog-graphql-go/internal/repository"
	"github.com/magendooro/magento2-go-common/middleware"
)

// CategoryService handles category listing, tree, and single-category queries.
type CategoryService struct {
	categoryRepo *repository.CategoryRepository
	storeConfig  *repository.StoreConfigRepository
}

func NewCategoryService(
	categoryRepo *repository.CategoryRepository,
	storeConfig *repository.StoreConfigRepository,
) *CategoryService {
	return &CategoryService{
		categoryRepo: categoryRepo,
		storeConfig:  storeConfig,
	}
}

// GetChildren returns the direct children of a category as CategoryTree nodes.
// Called by the CategoryTree.children field resolver — gqlgen recurses automatically
// for deeper nesting based on the client's query depth.
func (s *CategoryService) GetChildren(ctx context.Context, parentID int) ([]*model.CategoryTree, error) {
	storeID := middleware.GetStoreID(ctx)
	storeCfg := s.storeConfig.Get(storeID)

	children, err := s.categoryRepo.GetChildCategories(ctx, parentID, storeID)
	if err != nil {
		return nil, err
	}

	result := make([]*model.CategoryTree, 0, len(children))
	for _, c := range children {
		result = append(result, repository.BuildCategoryTree(c, storeCfg.CategoryURLSuffix))
	}
	return result, nil
}

// GetCategories returns a paginated, filtered list of categories.
func (s *CategoryService) GetCategories(ctx context.Context, filters *model.CategoryFilterInput, pageSize, currentPage int) (*model.CategoryResult, error) {
	storeID := middleware.GetStoreID(ctx)
	storeCfg := s.storeConfig.Get(storeID)

	repoFilters, err := parseFilters(filters)
	if err != nil {
		return nil, err
	}

	cats, total, err := s.categoryRepo.FindCategories(ctx, repoFilters, pageSize, currentPage, storeID)
	if err != nil {
		return nil, err
	}

	items := make([]*model.CategoryTree, 0, len(cats))
	for _, c := range cats {
		items = append(items, repository.BuildCategoryTree(c, storeCfg.CategoryURLSuffix))
	}

	totalPages := int(math.Ceil(float64(total) / float64(pageSize)))
	if totalPages < 1 && total == 0 {
		totalPages = 0
	}
	pageInfo := &model.SearchResultPageInfo{
		PageSize:    &pageSize,
		CurrentPage: &currentPage,
		TotalPages:  &totalPages,
	}

	return &model.CategoryResult{
		Items:      items,
		PageInfo:   pageInfo,
		TotalCount: &total,
	}, nil
}

// GetCategoryList returns a flat list of categories matching filters (categoryList query).
// Children (one level deep) are populated for each returned category.
func (s *CategoryService) GetCategoryList(ctx context.Context, filters *model.CategoryFilterInput) ([]*model.CategoryTree, error) {
	storeID := middleware.GetStoreID(ctx)
	storeCfg := s.storeConfig.Get(storeID)

	repoFilters, err := parseFilters(filters)
	if err != nil {
		return nil, err
	}

	cats, _, err := s.categoryRepo.FindCategories(ctx, repoFilters, 1000, 1, storeID)
	if err != nil {
		return nil, err
	}

	result := make([]*model.CategoryTree, 0, len(cats))
	for _, c := range cats {
		result = append(result, repository.BuildCategoryTree(c, storeCfg.CategoryURLSuffix))
	}
	return result, nil
}

// GetCategoryByID returns a single category tree by entity_id (deprecated `category` query).
func (s *CategoryService) GetCategoryByID(ctx context.Context, id int) (*model.CategoryTree, error) {
	storeID := middleware.GetStoreID(ctx)
	storeCfg := s.storeConfig.Get(storeID)

	c, err := s.categoryRepo.GetCategoryByID(ctx, id, storeID)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, nil
	}

	return repository.BuildCategoryTree(c, storeCfg.CategoryURLSuffix), nil
}

// parseFilters converts GraphQL CategoryFilterInput into repository CategoryFilters.
func parseFilters(f *model.CategoryFilterInput) (repository.CategoryFilters, error) {
	var filters repository.CategoryFilters
	if f == nil {
		return filters, nil
	}

	if f.Ids != nil {
		ids, err := parseEqualFilter(f.Ids)
		if err != nil {
			return filters, fmt.Errorf("invalid ids filter: %w", err)
		}
		for _, s := range ids {
			id, err := strconv.Atoi(s)
			if err != nil {
				return filters, fmt.Errorf("invalid category id %q: %w", s, err)
			}
			filters.IDs = append(filters.IDs, id)
		}
	}

	if f.CategoryUID != nil {
		uids, err := parseEqualFilter(f.CategoryUID)
		if err != nil {
			return filters, fmt.Errorf("invalid category_uid filter: %w", err)
		}
		for _, uid := range uids {
			id, err := decodeMagentoUID(uid)
			if err != nil {
				return filters, fmt.Errorf("invalid category_uid %q: %w", uid, err)
			}
			filters.IDs = append(filters.IDs, id)
		}
	}

	if f.ParentID != nil {
		parentIDs, err := parseEqualFilter(f.ParentID)
		if err != nil {
			return filters, fmt.Errorf("invalid parent_id filter: %w", err)
		}
		if len(parentIDs) > 0 {
			id, err := strconv.Atoi(parentIDs[0])
			if err != nil {
				return filters, fmt.Errorf("invalid parent_id %q: %w", parentIDs[0], err)
			}
			filters.ParentID = &id
		}
	}

	if f.ParentCategoryUID != nil {
		uids, err := parseEqualFilter(f.ParentCategoryUID)
		if err != nil {
			return filters, fmt.Errorf("invalid parent_category_uid filter: %w", err)
		}
		if len(uids) > 0 {
			id, err := decodeMagentoUID(uids[0])
			if err != nil {
				return filters, fmt.Errorf("invalid parent_category_uid %q: %w", uids[0], err)
			}
			filters.ParentID = &id
		}
	}

	if f.Name != nil && f.Name.Match != nil {
		filters.Name = f.Name.Match
	}

	if f.URLKey != nil {
		keys, err := parseEqualFilter(f.URLKey)
		if err != nil {
			return filters, fmt.Errorf("invalid url_key filter: %w", err)
		}
		if len(keys) > 0 {
			filters.URLKey = &keys[0]
		}
	}

	if f.URLPath != nil {
		paths, err := parseEqualFilter(f.URLPath)
		if err != nil {
			return filters, fmt.Errorf("invalid url_path filter: %w", err)
		}
		if len(paths) > 0 {
			filters.URLPath = &paths[0]
		}
	}

	return filters, nil
}

// parseEqualFilter extracts values from FilterEqualTypeInput (eq or in).
func parseEqualFilter(f *model.FilterEqualTypeInput) ([]string, error) {
	if f == nil {
		return nil, nil
	}
	if f.Eq != nil {
		return []string{*f.Eq}, nil
	}
	var vals []string
	for _, v := range f.In {
		if v != nil {
			vals = append(vals, *v)
		}
	}
	return vals, nil
}

// decodeMagentoUID base64-decodes a Magento category UID to its entity_id.
func decodeMagentoUID(uid string) (int, error) {
	b, err := base64.StdEncoding.DecodeString(uid)
	if err != nil {
		b, err = base64.RawStdEncoding.DecodeString(uid)
		if err != nil {
			return 0, fmt.Errorf("base64 decode failed: %w", err)
		}
	}
	return strconv.Atoi(strings.TrimSpace(string(b)))
}
