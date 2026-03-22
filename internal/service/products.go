package service

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/sync/errgroup"

	"github.com/magendooro/magento2-catalog-graphql-go/graph/model"
	"github.com/magendooro/magento2-catalog-graphql-go/internal/config"
	"github.com/magendooro/magento2-catalog-graphql-go/internal/middleware"
	"github.com/magendooro/magento2-catalog-graphql-go/internal/repository"
)

type ProductService struct {
	productRepo      *repository.ProductRepository
	priceRepo        *repository.PriceRepository
	mediaRepo        *repository.MediaRepository
	inventoryRepo    *repository.InventoryRepository
	categoryRepo     *repository.CategoryRepository
	urlRepo          *repository.URLRepository
	configurableRepo *repository.ConfigurableRepository
	bundleRepo       *repository.BundleRepository
	linkRepo         *repository.ProductLinkRepository
	aggregationRepo  *repository.AggregationRepository
	reviewRepo       *repository.ReviewRepository
	searchRepo       *repository.SearchRepository
	storeConfig      *repository.StoreConfigRepository
	cfg              *config.Config
}

func NewProductService(
	productRepo *repository.ProductRepository,
	priceRepo *repository.PriceRepository,
	mediaRepo *repository.MediaRepository,
	inventoryRepo *repository.InventoryRepository,
	categoryRepo *repository.CategoryRepository,
	urlRepo *repository.URLRepository,
	configurableRepo *repository.ConfigurableRepository,
	bundleRepo *repository.BundleRepository,
	linkRepo *repository.ProductLinkRepository,
	aggregationRepo *repository.AggregationRepository,
	reviewRepo *repository.ReviewRepository,
	searchRepo *repository.SearchRepository,
	storeConfig *repository.StoreConfigRepository,
	cfg *config.Config,
) *ProductService {
	return &ProductService{
		productRepo:      productRepo,
		priceRepo:        priceRepo,
		mediaRepo:        mediaRepo,
		inventoryRepo:    inventoryRepo,
		categoryRepo:     categoryRepo,
		urlRepo:          urlRepo,
		configurableRepo: configurableRepo,
		bundleRepo:       bundleRepo,
		linkRepo:         linkRepo,
		aggregationRepo:  aggregationRepo,
		reviewRepo:       reviewRepo,
		searchRepo:       searchRepo,
		storeConfig:      storeConfig,
		cfg:              cfg,
	}
}

func (s *ProductService) GetProducts(ctx context.Context, search *string, filter *model.ProductAttributeFilterInput, sort *model.ProductAttributeSortInput, pageSize, currentPage int) (*model.Products, error) {
	storeID := middleware.GetStoreID(ctx)

	if pageSize <= 0 {
		pageSize = 20
	}
	if currentPage <= 0 {
		currentPage = 1
	}

	// 1. Get base product data with EAV attributes (also returns all matching IDs for aggregation reuse)
	products, totalCount, allMatchingIDs, err := s.productRepo.FindProducts(ctx, storeID, search, filter, sort, pageSize, currentPage)
	if err != nil {
		return nil, err
	}

	// Determine which fields the client actually requested (must be before early return)
	rf := CollectRequestedFields(ctx)

	if len(products) == 0 {
		zero := 0
		result := &model.Products{
			Items:      []model.ProductInterface{},
			TotalCount: &totalCount,
			PageInfo: &model.SearchResultPageInfo{
				PageSize:    &pageSize,
				CurrentPage: &currentPage,
				TotalPages:  &zero,
			},
		}
		if rf.Aggregations {
			result.Aggregations = []*model.Aggregation{}
		}
		if rf.SortFields {
			result.SortFields = buildSortFields(search != nil && *search != "")
		}
		return result, nil
	}

	// Collect IDs for batch loading
	entityIDs := make([]int, len(products))
	rowIDs := make([]int, len(products))
	for i, p := range products {
		entityIDs[i] = p.EntityID
		rowIDs[i] = p.RowID
	}

	// 2. Load store config
	storeCfg := s.storeConfig.Get(ctx, storeID)
	currency := parseCurrency(storeCfg.BaseCurrency)

	// 3. Batch load only the data the client requested (parallel)
	var prices map[int]*repository.PriceData
	var tierPrices map[int][]*repository.TierPriceData
	var mediaGallery map[int][]*repository.MediaGalleryData
	var inventory map[int]*repository.InventoryData
	var categories map[int][]*repository.CategoryData
	var urlRewrites map[int][]*repository.URLRewriteData

	g, gctx := errgroup.WithContext(ctx)
	if rf.PriceRange {
		g.Go(func() error {
			var err error
			if prices, err = s.priceRepo.GetPricesForProducts(gctx, entityIDs, storeCfg.WebsiteID); err != nil {
				log.Warn().Err(err).Msg("failed to load prices")
			}
			return nil
		})
	}
	if rf.PriceTiers {
		g.Go(func() error {
			var err error
			if tierPrices, err = s.priceRepo.GetTierPricesForProducts(gctx, rowIDs, storeCfg.WebsiteID); err != nil {
				log.Warn().Err(err).Msg("failed to load tier prices")
			}
			return nil
		})
	}
	if rf.MediaGallery {
		g.Go(func() error {
			var err error
			if mediaGallery, err = s.mediaRepo.GetMediaForProducts(gctx, rowIDs, storeID); err != nil {
				log.Warn().Err(err).Msg("failed to load media gallery")
			}
			return nil
		})
	}
	if rf.Inventory {
		g.Go(func() error {
			var err error
			if inventory, err = s.inventoryRepo.GetInventoryForProducts(gctx, entityIDs); err != nil {
				log.Warn().Err(err).Msg("failed to load inventory")
			}
			return nil
		})
	}
	if rf.Categories {
		g.Go(func() error {
			var err error
			if categories, err = s.categoryRepo.GetCategoriesForProducts(gctx, entityIDs, storeID); err != nil {
				log.Warn().Err(err).Msg("failed to load categories")
			}
			return nil
		})
	}
	if rf.URLRewrites {
		g.Go(func() error {
			var err error
			if urlRewrites, err = s.urlRepo.GetURLRewritesForProducts(gctx, entityIDs, storeID); err != nil {
				log.Warn().Err(err).Msg("failed to load URL rewrites")
			}
			return nil
		})
	}
	_ = g.Wait()

	// 3b. Load type-specific data only if requested
	var configurableEntityIDs, bundleEntityIDs []int
	if rf.Configurable || rf.Bundle {
		for _, p := range products {
			switch p.TypeID {
			case "configurable":
				if rf.Configurable {
					configurableEntityIDs = append(configurableEntityIDs, p.EntityID)
				}
			case "bundle":
				if rf.Bundle {
					bundleEntityIDs = append(bundleEntityIDs, p.EntityID)
				}
			}
		}
	}

	var configData *configurableData
	if len(configurableEntityIDs) > 0 {
		configData = s.loadConfigurableData(ctx, configurableEntityIDs, storeID, storeCfg, currency)
	}

	var bData *bundleData
	if len(bundleEntityIDs) > 0 {
		bData = s.loadBundleData(ctx, bundleEntityIDs, storeID, storeCfg, currency)
	}

	// 3c. Load review summaries if requested
	var reviewSummaries map[int]*repository.ReviewSummaryData
	if rf.Reviews {
		if reviewSummaries, err = s.reviewRepo.GetReviewSummariesForProducts(ctx, entityIDs, storeID); err != nil {
			log.Warn().Err(err).Msg("failed to load review summaries")
		}
	}

	// 3d. Load related/upsell/crosssell product links only if requested
	var relData *relatedProductsData
	if rf.Related {
		relData = s.loadRelatedProducts(ctx, entityIDs, storeID, storeCfg, currency)
	}

	// 4. Map to GraphQL types
	items := make([]model.ProductInterface, 0, len(products))
	for _, p := range products {
		item := s.mapProductToModel(p, storeCfg, currency, prices, tierPrices, mediaGallery, inventory, categories, urlRewrites, configData, bData, relData, reviewSummaries)
		if item != nil {
			items = append(items, item)
		}
	}

	// 5. Load aggregations only if requested (reuse allMatchingIDs from FindProducts)
	var aggregations []*model.Aggregation
	if rf.Aggregations {
		aggregations = s.loadAggregations(ctx, storeID, allMatchingIDs, storeCfg)
		if aggregations == nil {
			aggregations = []*model.Aggregation{}
		}
	}

	// 6. Load search suggestions only if requested
	var suggestions []*model.SearchSuggestion
	if rf.Suggestions && search != nil && *search != "" {
		var suggData []*repository.SearchSuggestionData
		if suggData, err = s.searchRepo.GetSearchSuggestions(ctx, *search, storeID, 10); err != nil {
			log.Warn().Err(err).Msg("failed to load search suggestions")
		}
		for _, sd := range suggData {
			suggestions = append(suggestions, &model.SearchSuggestion{Search: sd.QueryText})
		}
	}

	// 7. Build sort_fields only if requested
	var sortFields *model.SortFields
	if rf.SortFields {
		sortFields = buildSortFields(search != nil && *search != "")
	}

	totalPages := int(math.Ceil(float64(totalCount) / float64(pageSize)))

	return &model.Products{
		Items: items,
		PageInfo: &model.SearchResultPageInfo{
			PageSize:    &pageSize,
			CurrentPage: &currentPage,
			TotalPages:  &totalPages,
		},
		TotalCount:   &totalCount,
		Aggregations: aggregations,
		SortFields:   sortFields,
		Suggestions:  suggestions,
	}, nil
}

func (s *ProductService) mapProductToModel(
	p *repository.ProductEAVValues,
	storeCfg *repository.StoreConfig,
	currency model.CurrencyEnum,
	prices map[int]*repository.PriceData,
	tierPrices map[int][]*repository.TierPriceData,
	mediaGallery map[int][]*repository.MediaGalleryData,
	inventory map[int]*repository.InventoryData,
	categories map[int][]*repository.CategoryData,
	urlRewrites map[int][]*repository.URLRewriteData,
	configData *configurableData,
	bData *bundleData,
	relData *relatedProductsData,
	reviewSummaries map[int]*repository.ReviewSummaryData,
) model.ProductInterface {
	base := s.buildProductBase(p, storeCfg, currency, prices, tierPrices, mediaGallery, inventory, categories, urlRewrites)

	// Attach related products (always non-nil arrays for Magento compatibility)
	base.relatedProducts = []model.ProductInterface{}
	base.upsellProducts = []model.ProductInterface{}
	base.crosssellProducts = []model.ProductInterface{}
	if relData != nil {
		if r := relData.related[p.EntityID]; r != nil {
			base.relatedProducts = r
		}
		if u := relData.upsell[p.EntityID]; u != nil {
			base.upsellProducts = u
		}
		if c := relData.crosssell[p.EntityID]; c != nil {
			base.crosssellProducts = c
		}
	}

	// Attach review data
	if reviewSummaries != nil {
		if rs, ok := reviewSummaries[p.EntityID]; ok {
			base.ratingSummary = float64(rs.RatingSummary)
			base.reviewCount = rs.ReviewCount
		}
	}
	// Always set reviews to empty ProductReviews (it's non-nullable in schema)
	base.reviews = &model.ProductReviews{
		Items: []*model.ProductReview{},
		PageInfo: &model.SearchResultPageInfo{
			PageSize:    intPtr(20),
			CurrentPage: intPtr(1),
			TotalPages:  intPtr(0),
		},
	}

	switch p.TypeID {
	case "simple":
		return base.toSimpleProduct()
	case "configurable":
		var variants []*model.ConfigurableVariant
		var configOptions []*model.ConfigurableProductOptions
		if configData != nil {
			variants = configData.variants[p.EntityID]
			configOptions = configData.options[p.EntityID]
		}
		return base.toConfigurableProduct(variants, configOptions)
	case "virtual":
		return base.toVirtualProduct()
	case "grouped":
		return base.toGroupedProduct()
	case "bundle":
		var bundleItems []*model.BundleItem
		var bundleAttrs *repository.BundleAttributeData
		if bData != nil {
			bundleItems = bData.items[p.EntityID]
			bundleAttrs = bData.attrs[p.EntityID]
		}
		return base.toBundleProduct(bundleItems, bundleAttrs)
	default:
		return base.toSimpleProduct()
	}
}

type productBase struct {
	id                   *int
	uid                  string
	name                 *string
	sku                  *string
	typeID               *string
	attributeSetID       *int
	description          *model.ComplexTextValue
	shortDescription     *model.ComplexTextValue
	specialPrice         *float64
	specialFromDate      *string
	specialToDate        *string
	metaTitle            *string
	metaKeyword          *string
	metaDescription      *string
	newFromDate          *string
	newToDate            *string
	createdAt            *string
	updatedAt            *string
	countryOfManufacture *string
	manufacturer         *int
	giftMessageAvailable *bool
	optionsContainer     *string
	urlKey               *string
	urlSuffix            *string
	canonicalURL         *string
	image                *model.ProductImage
	smallImage           *model.ProductImage
	thumbnail            *model.ProductImage
	swatchImage          *string
	weight               *float64
	priceRange           *model.PriceRange
	priceTiers           []*model.TierPrice
	mediaGallery         []model.MediaGalleryInterface
	stockStatus          *model.ProductStockStatus
	quantity             *float64
	minSaleQty           *float64
	maxSaleQty           *float64
	categories           []model.CategoryInterface
	urlRewrites          []*model.URLRewrite
	onlyXLeftInStock     *float64
	relatedProducts      []model.ProductInterface
	upsellProducts       []model.ProductInterface
	crosssellProducts    []model.ProductInterface
	ratingSummary        float64
	reviewCount          int
	reviews              *model.ProductReviews
}

func (b productBase) toSimpleProduct() *model.SimpleProduct {
	return &model.SimpleProduct{
		ID: b.id, UID: b.uid, Name: b.name, Sku: b.sku, TypeID: b.typeID,
		AttributeSetID: b.attributeSetID, Description: b.description, ShortDescription: b.shortDescription,
		SpecialPrice: b.specialPrice, SpecialFromDate: b.specialFromDate, SpecialToDate: b.specialToDate,
		MetaTitle: b.metaTitle, MetaKeyword: b.metaKeyword, MetaDescription: b.metaDescription,
		NewFromDate: b.newFromDate, NewToDate: b.newToDate, CreatedAt: b.createdAt, UpdatedAt: b.updatedAt,
		CountryOfManufacture: b.countryOfManufacture, Manufacturer: b.manufacturer,
		GiftMessageAvailable: b.giftMessageAvailable, OptionsContainer: b.optionsContainer,
		URLKey: b.urlKey, URLSuffix: b.urlSuffix, CanonicalURL: b.canonicalURL,
		Image: b.image, SmallImage: b.smallImage, Thumbnail: b.thumbnail, SwatchImage: b.swatchImage,
		Weight: b.weight, PriceRange: b.priceRange, PriceTiers: b.priceTiers,
		MediaGallery: b.mediaGallery, StockStatus: b.stockStatus,
		Quantity: b.quantity, MinSaleQty: b.minSaleQty, MaxSaleQty: b.maxSaleQty, OnlyXLeftInStock: b.onlyXLeftInStock,
		Categories: b.categories, URLRewrites: b.urlRewrites,
		RelatedProducts: b.relatedProducts, UpsellProducts: b.upsellProducts, CrosssellProducts: b.crosssellProducts,
		RatingSummary: b.ratingSummary, ReviewCount: b.reviewCount, Reviews: b.reviews,
	}
}

func (b productBase) toConfigurableProduct(variants []*model.ConfigurableVariant, configOptions []*model.ConfigurableProductOptions) *model.ConfigurableProduct {
	return &model.ConfigurableProduct{
		ID: b.id, UID: b.uid, Name: b.name, Sku: b.sku, TypeID: b.typeID,
		AttributeSetID: b.attributeSetID, Description: b.description, ShortDescription: b.shortDescription,
		SpecialPrice: b.specialPrice, SpecialFromDate: b.specialFromDate, SpecialToDate: b.specialToDate,
		MetaTitle: b.metaTitle, MetaKeyword: b.metaKeyword, MetaDescription: b.metaDescription,
		NewFromDate: b.newFromDate, NewToDate: b.newToDate, CreatedAt: b.createdAt, UpdatedAt: b.updatedAt,
		CountryOfManufacture: b.countryOfManufacture, Manufacturer: b.manufacturer,
		GiftMessageAvailable: b.giftMessageAvailable, OptionsContainer: b.optionsContainer,
		URLKey: b.urlKey, URLSuffix: b.urlSuffix, CanonicalURL: b.canonicalURL,
		Image: b.image, SmallImage: b.smallImage, Thumbnail: b.thumbnail, SwatchImage: b.swatchImage,
		Weight: b.weight, PriceRange: b.priceRange, PriceTiers: b.priceTiers,
		MediaGallery: b.mediaGallery, StockStatus: b.stockStatus,
		Quantity: b.quantity, MinSaleQty: b.minSaleQty, MaxSaleQty: b.maxSaleQty, OnlyXLeftInStock: b.onlyXLeftInStock,
		Categories: b.categories, URLRewrites: b.urlRewrites,
		RelatedProducts: b.relatedProducts, UpsellProducts: b.upsellProducts, CrosssellProducts: b.crosssellProducts,
		RatingSummary: b.ratingSummary, ReviewCount: b.reviewCount, Reviews: b.reviews,
		Variants: variants, ConfigurableOptions: configOptions,
	}
}

func (b productBase) toVirtualProduct() *model.VirtualProduct {
	return &model.VirtualProduct{
		ID: b.id, UID: b.uid, Name: b.name, Sku: b.sku, TypeID: b.typeID,
		AttributeSetID: b.attributeSetID, Description: b.description, ShortDescription: b.shortDescription,
		SpecialPrice: b.specialPrice, SpecialFromDate: b.specialFromDate, SpecialToDate: b.specialToDate,
		MetaTitle: b.metaTitle, MetaKeyword: b.metaKeyword, MetaDescription: b.metaDescription,
		NewFromDate: b.newFromDate, NewToDate: b.newToDate, CreatedAt: b.createdAt, UpdatedAt: b.updatedAt,
		CountryOfManufacture: b.countryOfManufacture, Manufacturer: b.manufacturer,
		GiftMessageAvailable: b.giftMessageAvailable, OptionsContainer: b.optionsContainer,
		URLKey: b.urlKey, URLSuffix: b.urlSuffix, CanonicalURL: b.canonicalURL,
		Image: b.image, SmallImage: b.smallImage, Thumbnail: b.thumbnail, SwatchImage: b.swatchImage,
		PriceRange: b.priceRange, PriceTiers: b.priceTiers,
		MediaGallery: b.mediaGallery, StockStatus: b.stockStatus,
		Quantity: b.quantity, MinSaleQty: b.minSaleQty, MaxSaleQty: b.maxSaleQty, OnlyXLeftInStock: b.onlyXLeftInStock,
		Categories: b.categories, URLRewrites: b.urlRewrites,
		RelatedProducts: b.relatedProducts, UpsellProducts: b.upsellProducts, CrosssellProducts: b.crosssellProducts,
		RatingSummary: b.ratingSummary, ReviewCount: b.reviewCount, Reviews: b.reviews,
	}
}

func (b productBase) toGroupedProduct() *model.GroupedProduct {
	return &model.GroupedProduct{
		ID: b.id, UID: b.uid, Name: b.name, Sku: b.sku, TypeID: b.typeID,
		AttributeSetID: b.attributeSetID, Description: b.description, ShortDescription: b.shortDescription,
		SpecialPrice: b.specialPrice, SpecialFromDate: b.specialFromDate, SpecialToDate: b.specialToDate,
		MetaTitle: b.metaTitle, MetaKeyword: b.metaKeyword, MetaDescription: b.metaDescription,
		NewFromDate: b.newFromDate, NewToDate: b.newToDate, CreatedAt: b.createdAt, UpdatedAt: b.updatedAt,
		CountryOfManufacture: b.countryOfManufacture, Manufacturer: b.manufacturer,
		GiftMessageAvailable: b.giftMessageAvailable, OptionsContainer: b.optionsContainer,
		URLKey: b.urlKey, URLSuffix: b.urlSuffix, CanonicalURL: b.canonicalURL,
		Image: b.image, SmallImage: b.smallImage, Thumbnail: b.thumbnail, SwatchImage: b.swatchImage,
		Weight: b.weight, PriceRange: b.priceRange, PriceTiers: b.priceTiers,
		MediaGallery: b.mediaGallery, StockStatus: b.stockStatus,
		Quantity: b.quantity, MinSaleQty: b.minSaleQty, MaxSaleQty: b.maxSaleQty, OnlyXLeftInStock: b.onlyXLeftInStock,
		Categories: b.categories, URLRewrites: b.urlRewrites,
		RelatedProducts: b.relatedProducts, UpsellProducts: b.upsellProducts, CrosssellProducts: b.crosssellProducts,
		RatingSummary: b.ratingSummary, ReviewCount: b.reviewCount, Reviews: b.reviews,
	}
}

func (b productBase) toBundleProduct(bundleItems []*model.BundleItem, bundleAttrs *repository.BundleAttributeData) *model.BundleProduct {
	bp := &model.BundleProduct{
		ID: b.id, UID: b.uid, Name: b.name, Sku: b.sku, TypeID: b.typeID,
		AttributeSetID: b.attributeSetID, Description: b.description, ShortDescription: b.shortDescription,
		SpecialPrice: b.specialPrice, SpecialFromDate: b.specialFromDate, SpecialToDate: b.specialToDate,
		MetaTitle: b.metaTitle, MetaKeyword: b.metaKeyword, MetaDescription: b.metaDescription,
		NewFromDate: b.newFromDate, NewToDate: b.newToDate, CreatedAt: b.createdAt, UpdatedAt: b.updatedAt,
		CountryOfManufacture: b.countryOfManufacture, Manufacturer: b.manufacturer,
		GiftMessageAvailable: b.giftMessageAvailable, OptionsContainer: b.optionsContainer,
		URLKey: b.urlKey, URLSuffix: b.urlSuffix, CanonicalURL: b.canonicalURL,
		Image: b.image, SmallImage: b.smallImage, Thumbnail: b.thumbnail, SwatchImage: b.swatchImage,
		Weight: b.weight, PriceRange: b.priceRange, PriceTiers: b.priceTiers,
		MediaGallery: b.mediaGallery, StockStatus: b.stockStatus,
		Quantity: b.quantity, MinSaleQty: b.minSaleQty, MaxSaleQty: b.maxSaleQty, OnlyXLeftInStock: b.onlyXLeftInStock,
		Categories: b.categories, URLRewrites: b.urlRewrites,
		RelatedProducts: b.relatedProducts, UpsellProducts: b.upsellProducts, CrosssellProducts: b.crosssellProducts,
		RatingSummary: b.ratingSummary, ReviewCount: b.reviewCount, Reviews: b.reviews,
		Items: bundleItems,
	}
	if bundleAttrs != nil {
		bp.DynamicPrice = &bundleAttrs.DynamicPrice
		bp.DynamicSku = &bundleAttrs.DynamicSku
		bp.DynamicWeight = &bundleAttrs.DynamicWeight
		if bundleAttrs.PriceView == 0 {
			pv := model.PriceViewEnumPriceRange
			bp.PriceView = &pv
		} else {
			pv := model.PriceViewEnumAsLowAs
			bp.PriceView = &pv
		}
		if bundleAttrs.ShipmentType == 0 {
			st := model.ShipBundleItemsEnumTogether
			bp.ShipBundleItems = &st
		} else {
			st := model.ShipBundleItemsEnumSeparately
			bp.ShipBundleItems = &st
		}
	}
	return bp
}

func toComplexText(s *string) *model.ComplexTextValue {
	if s == nil {
		return &model.ComplexTextValue{HTML: ""}
	}
	return &model.ComplexTextValue{HTML: *s}
}

// toBoolFromEAV converts a Magento EAV Boolean source value to a Go bool.
// Magento Boolean source: "0" = No, "1" = Yes, "2" = Use config (resolved to false by default).
func toBoolFromEAV(s *string) *bool {
	if s == nil {
		return nil
	}
	v := *s == "1"
	return &v
}

func toProductImage(path *string, baseURL string, labelFallback *string) *model.ProductImage {
	if path == nil || *path == "" || *path == "no_selection" {
		return nil
	}
	url := baseURL + *path
	return &model.ProductImage{URL: &url, Label: labelFallback}
}

// configurableData holds pre-loaded configurable product data for batch mapping.
type configurableData struct {
	variants map[int][]*model.ConfigurableVariant         // parentEntityID → variants
	options  map[int][]*model.ConfigurableProductOptions  // parentEntityID → configurable options
}

// loadConfigurableData batch-loads all configurable product data for the given parent entity IDs.
func (s *ProductService) loadConfigurableData(ctx context.Context, parentEntityIDs []int, storeID int, storeCfg *repository.StoreConfig, currency model.CurrencyEnum) *configurableData {
	result := &configurableData{
		variants: make(map[int][]*model.ConfigurableVariant),
		options:  make(map[int][]*model.ConfigurableProductOptions),
	}

	// 1. Load super attributes (which attributes are configurable for each parent)
	superAttrs, err := s.configurableRepo.GetSuperAttributesForProducts(ctx, parentEntityIDs, storeID)
	if err != nil {
		return result
	}
	if len(superAttrs) == 0 {
		return result
	}

	// 2. Load super links (parent → child entity IDs)
	superLinks, err := s.configurableRepo.GetSuperLinksForProducts(ctx, parentEntityIDs)
	if err != nil || len(superLinks) == 0 {
		return result
	}

	// Collect all child entity IDs and all super attribute IDs
	childIDSet := make(map[int]bool)
	attrIDSet := make(map[int]bool)
	for _, children := range superLinks {
		for _, childID := range children {
			childIDSet[childID] = true
		}
	}
	for _, attrs := range superAttrs {
		for _, sa := range attrs {
			attrIDSet[sa.AttributeID] = true
		}
	}

	allChildIDs := make([]int, 0, len(childIDSet))
	for id := range childIDSet {
		allChildIDs = append(allChildIDs, id)
	}
	allAttrIDs := make([]int, 0, len(attrIDSet))
	for id := range attrIDSet {
		allAttrIDs = append(allAttrIDs, id)
	}

	// 3. Load child products EAV data
	childProducts, err := s.configurableRepo.GetChildProductsEAV(ctx, allChildIDs, storeID)
	if err != nil {
		return result
	}
	childByID := make(map[int]*repository.ProductEAVValues, len(childProducts))
	childRowIDs := make([]int, 0, len(childProducts))
	for _, cp := range childProducts {
		childByID[cp.EntityID] = cp
		childRowIDs = append(childRowIDs, cp.RowID)
	}

	// 4. Load child attribute values (which option_id each child has for each super attribute)
	childAttrValues, _ := s.configurableRepo.GetChildAttributeValues(ctx, allChildIDs, allAttrIDs)

	// 5. Collect all option IDs for label and swatch loading
	optionIDSet := make(map[int]bool)
	for _, attrMap := range childAttrValues {
		for _, optionID := range attrMap {
			optionIDSet[optionID] = true
		}
	}
	allOptionIDs := make([]int, 0, len(optionIDSet))
	for id := range optionIDSet {
		allOptionIDs = append(allOptionIDs, id)
	}

	// 6. Load option labels and swatches
	optionLabels, _ := s.configurableRepo.GetAttributeOptionLabels(ctx, allOptionIDs, storeID)
	swatches, _ := s.configurableRepo.GetSwatchesForOptions(ctx, allOptionIDs, storeID)

	// 7. Load related data for child products (prices, media, inventory)
	childPrices, _ := s.priceRepo.GetPricesForProducts(ctx, allChildIDs, storeCfg.WebsiteID)
	childTierPrices, _ := s.priceRepo.GetTierPricesForProducts(ctx, childRowIDs, storeCfg.WebsiteID)
	childMedia, _ := s.mediaRepo.GetMediaForProducts(ctx, childRowIDs, storeID)
	childInventory, _ := s.inventoryRepo.GetInventoryForProducts(ctx, allChildIDs)

	// 8. Build ConfigurableProductOptions for each parent
	for parentID, attrs := range superAttrs {
		var opts []*model.ConfigurableProductOptions
		children := superLinks[parentID]

		for _, sa := range attrs {
			// Collect unique option values used by children of this parent
			usedOptions := make(map[int]bool)
			for _, childID := range children {
				if attrMap, ok := childAttrValues[childID]; ok {
					if optID, ok := attrMap[sa.AttributeID]; ok {
						usedOptions[optID] = true
					}
				}
			}

			var values []*model.ConfigurableProductOptionsValues
			for optID := range usedOptions {
				label := optionLabels[optID]
				uid := repository.EncodeConfigurableUID(sa.AttributeID, optID)
				useDefault := false
				val := &model.ConfigurableProductOptionsValues{
					ValueIndex:      &optID,
					UID:             &uid,
					Label:           &label,
					DefaultLabel:    &label,
					StoreLabel:      &label,
					UseDefaultValue: &useDefault,
				}
				// Swatch data
				if sw, ok := swatches[optID]; ok {
					val.SwatchData = buildSwatchData(sw)
				}
				values = append(values, val)
			}

			attrIDStr := fmt.Sprintf("%d", sa.AttributeID)
			attrUID := repository.EncodeMagentoUID(sa.AttributeID)
			opt := &model.ConfigurableProductOptions{
				ID:            &sa.ProductSuperAttributeID,
				UID:           repository.EncodeMagentoUID(sa.ProductSuperAttributeID),
				AttributeID:   &attrIDStr,
				AttributeIDV2: &sa.AttributeID,
				AttributeUID:  attrUID,
				AttributeCode: &sa.AttributeCode,
				Label:         &sa.Label,
				Position:      &sa.Position,
				UseDefault:    &sa.UseDefault,
				Values:        values,
				ProductID:     &parentID,
			}
			opts = append(opts, opt)
		}
		result.options[parentID] = opts
	}

	// 9. Build ConfigurableVariants for each parent
	for parentID, children := range superLinks {
		attrs := superAttrs[parentID]
		var variants []*model.ConfigurableVariant

		for _, childID := range children {
			childEAV, ok := childByID[childID]
			if !ok {
				continue
			}

			// Build the child SimpleProduct
			childBase := s.buildProductBase(childEAV, storeCfg, currency, childPrices, childTierPrices, childMedia, childInventory, nil, nil)
			simpleProduct := childBase.toSimpleProduct()

			// Build ConfigurableAttributeOption for each super attribute
			var attrOptions []*model.ConfigurableAttributeOption
			for _, sa := range attrs {
				if attrMap, ok := childAttrValues[childID]; ok {
					if optID, ok := attrMap[sa.AttributeID]; ok {
						label := optionLabels[optID]
						uid := repository.EncodeConfigurableUID(sa.AttributeID, optID)
						attrOptions = append(attrOptions, &model.ConfigurableAttributeOption{
							Label:      &label,
							Code:       &sa.AttributeCode,
							ValueIndex: &optID,
							UID:        uid,
						})
					}
				}
			}

			variants = append(variants, &model.ConfigurableVariant{
				Attributes: attrOptions,
				Product:    simpleProduct,
			})
		}
		result.variants[parentID] = variants
	}

	return result
}

// buildProductBase creates a productBase from EAV values and related data, reusable for both
// parent products and configurable child products.
func (s *ProductService) buildProductBase(
	p *repository.ProductEAVValues,
	storeCfg *repository.StoreConfig,
	currency model.CurrencyEnum,
	prices map[int]*repository.PriceData,
	tierPrices map[int][]*repository.TierPriceData,
	mediaGallery map[int][]*repository.MediaGalleryData,
	inventory map[int]*repository.InventoryData,
	categories map[int][]*repository.CategoryData,
	urlRewrites map[int][]*repository.URLRewriteData,
) productBase {
	uid := repository.EncodeMagentoUID(p.EntityID)
	typeID := p.TypeID

	priceData := prices[p.EntityID]
	priceRange := repository.BuildPriceRange(priceData, currency)

	regularPrice := 0.0
	if priceData != nil && priceData.Price != nil {
		regularPrice = *priceData.Price
	}
	priceTiers := repository.BuildTierPrices(tierPrices[p.RowID], regularPrice, currency)

	mediaBaseURL := storeCfg.MediaBaseURL
	if s.cfg.Media.BaseURL != "" {
		mediaBaseURL = s.cfg.Media.BaseURL // config override takes precedence
	}
	gallery := repository.BuildMediaGallery(mediaGallery[p.RowID], mediaBaseURL, p.Name)

	inv := inventory[p.EntityID]
	stockStatus := repository.BuildStockStatus(inv)
	var qty, minSaleQty, maxSaleQty, onlyXLeft *float64
	if inv != nil {
		qty = &inv.Qty
		minSaleQty = &inv.MinSaleQty
		maxSaleQty = &inv.MaxSaleQty
		if storeCfg.StockThresholdQty > 0 && inv.Qty > 0 && inv.Qty <= storeCfg.StockThresholdQty {
			onlyXLeft = &inv.Qty
		}
	}

	cats := make([]model.CategoryInterface, 0)
	if categories != nil {
		if catList, ok := categories[p.EntityID]; ok {
			cats = make([]model.CategoryInterface, 0, len(catList))
			for _, c := range catList {
				cats = append(cats, repository.BuildCategoryTree(c, storeCfg.CategoryURLSuffix))
			}
		}
	}

	urlRW := make([]*model.URLRewrite, 0)
	if urlRewrites != nil {
		urlRW = repository.BuildURLRewrites(urlRewrites[p.EntityID])
		if urlRW == nil {
			urlRW = make([]*model.URLRewrite, 0)
		}
	}

	urlSuffix := storeCfg.ProductURLSuffix

	var canonicalURL *string
	if storeCfg.ProductCanonicalTag && p.URLKey != nil {
		u := *p.URLKey + urlSuffix
		canonicalURL = &u
	}

	return productBase{
		id:                   &p.EntityID,
		uid:                  uid,
		name:                 p.Name,
		sku:                  &p.SKU,
		typeID:               &typeID,
		attributeSetID:       &p.AttributeSetID,
		description:          toComplexText(p.Description),
		shortDescription:     toComplexText(p.ShortDescription),
		specialPrice:         p.SpecialPrice,
		specialFromDate:      p.SpecialFromDate,
		specialToDate:        p.SpecialToDate,
		metaTitle:            p.MetaTitle,
		metaKeyword:          p.MetaKeyword,
		metaDescription:      p.MetaDescription,
		newFromDate:          p.NewFromDate,
		newToDate:            p.NewToDate,
		createdAt:            strPtr(formatMagentoDate(p.CreatedAt)),
		updatedAt:            strPtr(formatMagentoDate(p.UpdatedAt)),
		countryOfManufacture: p.CountryOfMfg,
		manufacturer:         p.Manufacturer,
		giftMessageAvailable: toBoolFromEAV(p.GiftMsgAvail),
		optionsContainer:     p.OptionsContainer,
		urlKey:               p.URLKey,
		urlSuffix:            &urlSuffix,
		canonicalURL:         canonicalURL,
		image:                toProductImage(p.Image, mediaBaseURL, p.Name),
		smallImage:           toProductImage(p.SmallImage, mediaBaseURL, p.Name),
		thumbnail:            toProductImage(p.Thumbnail, mediaBaseURL, p.Name),
		swatchImage:          p.SwatchImage,
		weight:               p.Weight,
		priceRange:           priceRange,
		priceTiers:           priceTiers,
		mediaGallery:         gallery,
		stockStatus:          stockStatus,
		quantity:             qty,
		minSaleQty:           minSaleQty,
		maxSaleQty:           maxSaleQty,
		onlyXLeftInStock:     onlyXLeft,
		categories:           cats,
		urlRewrites:          urlRW,
	}
}

func buildSwatchData(sw *repository.SwatchData) model.SwatchDataInterface {
	switch sw.SwatchType {
	case 1: // color
		return model.ColorSwatchData{Value: &sw.Value}
	case 2: // image
		return model.ImageSwatchData{Value: &sw.Value}
	case 3: // text
		return model.TextSwatchData{Value: &sw.Value}
	default:
		return nil
	}
}

// bundleData holds pre-loaded bundle product data for batch mapping.
type bundleData struct {
	items map[int][]*model.BundleItem             // parentEntityID → bundle items
	attrs map[int]*repository.BundleAttributeData  // parentEntityID → bundle attributes
}

// loadBundleData batch-loads all bundle product data for the given parent entity IDs.
func (s *ProductService) loadBundleData(ctx context.Context, parentEntityIDs []int, storeID int, storeCfg *repository.StoreConfig, currency model.CurrencyEnum) *bundleData {
	result := &bundleData{
		items: make(map[int][]*model.BundleItem),
		attrs: make(map[int]*repository.BundleAttributeData),
	}

	// 1. Load bundle options
	bundleOptions, err := s.bundleRepo.GetBundleOptionsForProducts(ctx, parentEntityIDs, storeID)
	if err != nil || len(bundleOptions) == 0 {
		return result
	}

	// 2. Collect all option IDs
	var allOptionIDs []int
	for _, opts := range bundleOptions {
		for _, opt := range opts {
			allOptionIDs = append(allOptionIDs, opt.OptionID)
		}
	}

	// 3. Load bundle selections
	selections, err := s.bundleRepo.GetBundleSelectionsForOptions(ctx, allOptionIDs)
	if err != nil {
		return result
	}

	// 4. Collect all child product IDs from selections
	childIDSet := make(map[int]bool)
	for _, sels := range selections {
		for _, sel := range sels {
			childIDSet[sel.ProductID] = true
		}
	}
	allChildIDs := make([]int, 0, len(childIDSet))
	for id := range childIDSet {
		allChildIDs = append(allChildIDs, id)
	}

	// 5. Load child product names (minimal data for bundle option labels)
	childProducts, _ := s.configurableRepo.GetChildProductsEAV(ctx, allChildIDs, storeID)
	childByID := make(map[int]*repository.ProductEAVValues, len(childProducts))
	childRowIDs := make([]int, 0, len(childProducts))
	for _, cp := range childProducts {
		childByID[cp.EntityID] = cp
		childRowIDs = append(childRowIDs, cp.RowID)
	}

	// 6. Load child prices for BundleItemOption.product
	childPrices, _ := s.priceRepo.GetPricesForProducts(ctx, allChildIDs, storeCfg.WebsiteID)
	childTierPrices, _ := s.priceRepo.GetTierPricesForProducts(ctx, childRowIDs, storeCfg.WebsiteID)
	childMedia, _ := s.mediaRepo.GetMediaForProducts(ctx, childRowIDs, storeID)
	childInventory, _ := s.inventoryRepo.GetInventoryForProducts(ctx, allChildIDs)

	// 7. Load bundle-specific attributes
	bundleAttrs, _ := s.bundleRepo.GetBundleAttributesForProducts(ctx, parentEntityIDs)
	result.attrs = bundleAttrs

	// 8. Build BundleItems for each parent
	for parentID, opts := range bundleOptions {
		var items []*model.BundleItem
		for _, opt := range opts {
			required := opt.Required
			position := opt.Position

			// Build BundleItemOptions (selections)
			var options []*model.BundleItemOption
			sels := selections[opt.OptionID]
			for _, sel := range sels {
				isDefault := sel.IsDefault
				qty := sel.SelectionQty
				selPosition := sel.Position
				canChangeQty := sel.CanChangeQty
				price := sel.SelectionPrice

				var priceType *model.PriceTypeEnum
				if sel.SelectionPriceType == 0 {
					pt := model.PriceTypeEnumFixed
					priceType = &pt
				} else {
					pt := model.PriceTypeEnumPercent
					priceType = &pt
				}

				var childProduct model.ProductInterface
				if cp, ok := childByID[sel.ProductID]; ok {
					childBase := s.buildProductBase(cp, storeCfg, currency, childPrices, childTierPrices, childMedia, childInventory, nil, nil)
					childProduct = childBase.toSimpleProduct()
				}

				var label *string
				if cp, ok := childByID[sel.ProductID]; ok && cp.Name != nil {
					label = cp.Name
				}

				selQtyInt := int(sel.SelectionQty)
				bioption := &model.BundleItemOption{
					ID:                &sel.SelectionID,
					UID:               repository.EncodeBundleOptionUID(opt.OptionID, sel.SelectionID, selQtyInt),
					Label:             label,
					Qty:               &qty,
					Quantity:          &qty,
					Position:          &selPosition,
					IsDefault:         &isDefault,
					Price:             &price,
					PriceType:         priceType,
					CanChangeQuantity: &canChangeQty,
					Product:           childProduct,
				}
				options = append(options, bioption)
			}

			items = append(items, &model.BundleItem{
				OptionID: &opt.OptionID,
				UID:      repository.EncodeBundleItemUID(opt.OptionID),
				Title:    &opt.Title,
				Required: &required,
				Type:     &opt.Type,
				Position: &position,
				Options:  options,
			})
		}
		result.items[parentID] = items
	}

	return result
}

// relatedProductsData holds pre-loaded related/upsell/crosssell products.
type relatedProductsData struct {
	related   map[int][]model.ProductInterface // entityID → related products
	upsell    map[int][]model.ProductInterface // entityID → upsell products
	crosssell map[int][]model.ProductInterface // entityID → crosssell products
}

// loadRelatedProducts batch-loads related, upsell, and crosssell products.
func (s *ProductService) loadRelatedProducts(ctx context.Context, entityIDs []int, storeID int, storeCfg *repository.StoreConfig, currency model.CurrencyEnum) *relatedProductsData {
	result := &relatedProductsData{
		related:   make(map[int][]model.ProductInterface),
		upsell:    make(map[int][]model.ProductInterface),
		crosssell: make(map[int][]model.ProductInterface),
	}

	relatedLinks, upsellLinks, crosssellLinks, err := s.linkRepo.GetAllLinksForProducts(ctx, entityIDs)
	if err != nil {
		return result
	}

	// Collect all linked product IDs
	linkedIDSet := make(map[int]bool)
	for _, links := range []map[int][]int{relatedLinks, upsellLinks, crosssellLinks} {
		for _, ids := range links {
			for _, id := range ids {
				linkedIDSet[id] = true
			}
		}
	}

	if len(linkedIDSet) == 0 {
		return result
	}

	allLinkedIDs := make([]int, 0, len(linkedIDSet))
	for id := range linkedIDSet {
		allLinkedIDs = append(allLinkedIDs, id)
	}

	// Load linked products as simple EAV
	linkedProducts, _ := s.configurableRepo.GetChildProductsEAV(ctx, allLinkedIDs, storeID)
	linkedByID := make(map[int]*repository.ProductEAVValues, len(linkedProducts))
	linkedRowIDs := make([]int, 0, len(linkedProducts))
	for _, lp := range linkedProducts {
		linkedByID[lp.EntityID] = lp
		linkedRowIDs = append(linkedRowIDs, lp.RowID)
	}

	// Load prices and media for linked products
	linkedPrices, _ := s.priceRepo.GetPricesForProducts(ctx, allLinkedIDs, storeCfg.WebsiteID)
	linkedTierPrices, _ := s.priceRepo.GetTierPricesForProducts(ctx, linkedRowIDs, storeCfg.WebsiteID)
	linkedMedia, _ := s.mediaRepo.GetMediaForProducts(ctx, linkedRowIDs, storeID)
	linkedInventory, _ := s.inventoryRepo.GetInventoryForProducts(ctx, allLinkedIDs)

	// Build product interfaces for linked products
	buildLinkedProduct := func(entityID int) model.ProductInterface {
		lp, ok := linkedByID[entityID]
		if !ok {
			return nil
		}
		base := s.buildProductBase(lp, storeCfg, currency, linkedPrices, linkedTierPrices, linkedMedia, linkedInventory, nil, nil)
		return base.toSimpleProduct()
	}

	// Map linked products by source product
	for srcID, linkedIDs := range relatedLinks {
		for _, id := range linkedIDs {
			if p := buildLinkedProduct(id); p != nil {
				result.related[srcID] = append(result.related[srcID], p)
			}
		}
	}
	for srcID, linkedIDs := range upsellLinks {
		for _, id := range linkedIDs {
			if p := buildLinkedProduct(id); p != nil {
				result.upsell[srcID] = append(result.upsell[srcID], p)
			}
		}
	}
	for srcID, linkedIDs := range crosssellLinks {
		for _, id := range linkedIDs {
			if p := buildLinkedProduct(id); p != nil {
				result.crosssell[srcID] = append(result.crosssell[srcID], p)
			}
		}
	}

	return result
}

// loadAggregations computes faceted search aggregation buckets.
// matchingIDs are all entity IDs matching the filter (from FindProducts), avoiding a duplicate query.
func (s *ProductService) loadAggregations(ctx context.Context, storeID int, matchingIDs []int, storeCfg *repository.StoreConfig) []*model.Aggregation {
	if len(matchingIDs) == 0 {
		return nil
	}

	// Get filterable attributes
	filterableAttrs, err := s.aggregationRepo.GetFilterableAttributes(ctx, false)
	if err != nil {
		return nil
	}

	var aggregations []*model.Aggregation

	// Add category aggregation
	catBucket, _ := s.aggregationRepo.GetCategoryAggregation(ctx, matchingIDs, storeID)
	if catBucket != nil {
		aggregations = append(aggregations, bucketToAggregation(catBucket))
	}

	for _, attr := range filterableAttrs {
		if attr.FrontendInput == "price" {
			// Price aggregation
			priceBucket, _ := s.aggregationRepo.GetPriceAggregation(ctx, matchingIDs, storeCfg.WebsiteID)
			if priceBucket != nil {
				aggregations = append(aggregations, bucketToAggregation(priceBucket))
			}
		} else if attr.FrontendInput == "select" || attr.FrontendInput == "multiselect" || attr.FrontendInput == "boolean" {
			// Select/multiselect aggregation using EAV index
			bucket, _ := s.aggregationRepo.GetSelectAggregations(ctx, attr, matchingIDs, storeID)
			if bucket != nil {
				aggregations = append(aggregations, bucketToAggregation(bucket))
			}
		}
	}

	return aggregations
}

func bucketToAggregation(bucket *repository.AggregationBucket) *model.Aggregation {
	count := len(bucket.Options)
	position := bucket.Position
	options := make([]*model.AggregationOption, len(bucket.Options))
	for i, opt := range bucket.Options {
		optCount := opt.Count
		label := opt.Label
		options[i] = &model.AggregationOption{
			Count: &optCount,
			Label: &label,
			Value: opt.Value,
		}
	}
	return &model.Aggregation{
		Count:         &count,
		Label:         &bucket.Label,
		AttributeCode: bucket.AttributeCode,
		Options:       options,
		Position:      &position,
	}
}

func buildSortFields(hasSearch bool) *model.SortFields {
	defaultSort := "position"
	options := []*model.SortField{
		{Value: strPtr("position"), Label: strPtr("Position")},
		{Value: strPtr("name"), Label: strPtr("Product Name")},
		{Value: strPtr("price"), Label: strPtr("Price")},
	}
	if hasSearch {
		options = append(options, &model.SortField{Value: strPtr("relevance"), Label: strPtr("Relevance")})
	}
	return &model.SortFields{
		Default: &defaultSort,
		Options: options,
	}
}

func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }

// formatMagentoDate converts Go time strings (RFC3339 from MySQL driver) to Magento format.
// Input: "2025-07-23T15:55:30Z" → Output: "2025-07-23 15:55:30"
func formatMagentoDate(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		// Try MySQL format directly
		t, err = time.Parse("2006-01-02 15:04:05", s)
		if err != nil {
			return s
		}
	}
	return t.Format("2006-01-02 15:04:05")
}

func parseCurrency(code string) model.CurrencyEnum {
	switch code {
	case "AED":
		return model.CurrencyEnumAed
	case "USD":
		return model.CurrencyEnumUsd
	case "EUR":
		return model.CurrencyEnumEur
	case "GBP":
		return model.CurrencyEnumGbp
	case "SAR":
		return model.CurrencyEnumSar
	default:
		return model.CurrencyEnum(code)
	}
}
