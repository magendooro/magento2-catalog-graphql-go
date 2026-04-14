package graph

import (
	"context"
	"database/sql"
	"fmt"

	localconfig "github.com/magendooro/magento2-catalog-graphql-go/internal/config"
	"github.com/magendooro/magento2-catalog-graphql-go/internal/repository"
	essearch "github.com/magendooro/magento2-catalog-graphql-go/internal/search"
	"github.com/magendooro/magento2-catalog-graphql-go/internal/service"
	commonconfig "github.com/magendooro/magento2-go-common/config"
)

// Resolver is the root resolver. It holds dependencies shared across all resolvers.
type Resolver struct {
	ProductService  *service.ProductService
	CategoryService *service.CategoryService
	ReviewService   *service.ReviewService
}

func NewResolver(db *sql.DB, cfg *localconfig.Config) (*Resolver, error) {
	cp, err := commonconfig.NewConfigProvider(db)
	if err != nil {
		return nil, fmt.Errorf("failed to load config provider: %w", err)
	}

	attrRepo := repository.NewAttributeRepository(db)
	if err := attrRepo.LoadProductAttributes(context.Background()); err != nil {
		return nil, fmt.Errorf("failed to load attribute metadata: %w", err)
	}

	productRepo := repository.NewProductRepository(db, attrRepo)
	priceRepo := repository.NewPriceRepository(db)
	mediaRepo := repository.NewMediaRepository(db)
	inventoryRepo := repository.NewInventoryRepository(db)
	categoryRepo := repository.NewCategoryRepository(db)
	urlRepo := repository.NewURLRepository(db)
	configurableRepo := repository.NewConfigurableRepository(db, attrRepo)
	bundleRepo := repository.NewBundleRepository(db, attrRepo)
	linkRepo := repository.NewProductLinkRepository(db)
	aggregationRepo := repository.NewAggregationRepository(db, attrRepo)
	reviewRepo := repository.NewReviewRepository(db)
	searchRepo := repository.NewSearchRepository(db)
	storeConfigRepo := repository.NewStoreConfigRepository(cp)

	productService := service.NewProductService(
		productRepo, priceRepo, mediaRepo, inventoryRepo,
		categoryRepo, urlRepo, configurableRepo, bundleRepo, linkRepo, aggregationRepo, reviewRepo, searchRepo, storeConfigRepo, cfg,
	)

	searchClient := essearch.NewClient(cp)
	if searchClient != nil {
		productService.SetSearchClient(searchClient)
	}

	categoryService := service.NewCategoryService(categoryRepo, storeConfigRepo)
	reviewService := service.NewReviewService(reviewRepo, productRepo)

	return &Resolver{
		ProductService:  productService,
		CategoryService: categoryService,
		ReviewService:   reviewService,
	}, nil
}
