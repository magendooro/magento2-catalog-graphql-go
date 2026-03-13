package graph

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/magendooro/magento2-catalog-graphql-go/internal/config"
	"github.com/magendooro/magento2-catalog-graphql-go/internal/repository"
	"github.com/magendooro/magento2-catalog-graphql-go/internal/service"
)

// Resolver is the root resolver. It holds dependencies shared across all resolvers.
type Resolver struct {
	ProductService *service.ProductService
}

func NewResolver(db *sql.DB, cfg *config.Config) (*Resolver, error) {
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
	storeConfigRepo := repository.NewStoreConfigRepository(db)

	productService := service.NewProductService(
		productRepo, priceRepo, mediaRepo, inventoryRepo,
		categoryRepo, urlRepo, configurableRepo, bundleRepo, linkRepo, aggregationRepo, reviewRepo, searchRepo, storeConfigRepo, cfg,
	)

	return &Resolver{
		ProductService: productService,
	}, nil
}
