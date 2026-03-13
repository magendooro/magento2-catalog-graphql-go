# Production Deployment

## Overview

This service is designed as a **drop-in accelerator** for Magento 2's `products()` GraphQL query. In production, it runs alongside Magento behind a reverse proxy or CDN that routes only `products()` queries to Go while all other GraphQL operations (cart, checkout, customer, CMS) continue hitting Magento PHP.

## Architecture

```
                    ┌─────────────────────┐
                    │   Client / Storefront│
                    └──────────┬──────────┘
                               │
                    ┌──────────▼──────────┐
                    │  Cloudflare / Nginx  │
                    │   (GraphQL Router)   │
                    └───┬─────────────┬───┘
                        │             │
              products()│             │ everything else
                        │             │
               ┌────────▼───┐   ┌─────▼────────┐
               │  Go Service │   │  Magento PHP  │
               │  :8080      │   │  :443         │
               └──────┬─────┘   └──────┬────────┘
                      │                │
                      └───────┬────────┘
                              │
                    ┌─────────▼─────────┐
                    │   MySQL (shared)   │
                    └───────────────────┘
```

Both services read from the **same MySQL database**. The Go service is read-only and never writes to the database.

## GraphQL Query Routing

Since all Magento GraphQL queries go to the same `/graphql` endpoint, the router must inspect the request body to determine which backend should handle it.

### Strategy

The router parses the GraphQL query string and checks if it contains **only** the `products` operation. If so, it routes to Go. Everything else goes to Magento.

A simple and reliable approach: check if the query body matches `products(` and does **not** contain other root-level query types (e.g., `cart`, `customer`, `categories`, `cmsPage`).

### Cloudflare Worker

A Cloudflare Worker sits at the edge and routes requests before they reach your origin. This gives you sub-millisecond routing with no additional infrastructure.

```javascript
// Cloudflare Worker: GraphQL query router
const GO_ORIGIN = "https://go-catalog.your-internal-domain.com";
const MAGENTO_ORIGIN = "https://magento.your-domain.com";

export default {
  async fetch(request, env) {
    const url = new URL(request.url);

    // Only intercept POST /graphql
    if (request.method !== "POST" || !url.pathname.endsWith("/graphql")) {
      return fetch(request, { cf: { resolveOverride: MAGENTO_ORIGIN } });
    }

    const body = await request.text();
    const backend = shouldRouteToGo(body) ? GO_ORIGIN : MAGENTO_ORIGIN;

    // Forward the request with all original headers (including Store header)
    const newRequest = new Request(backend + url.pathname, {
      method: "POST",
      headers: request.headers,
      body: body,
    });

    return fetch(newRequest);
  },
};

function shouldRouteToGo(body) {
  try {
    const parsed = JSON.parse(body);
    const query = parsed.query || "";

    // Route to Go if the query contains products() and nothing else
    // at the root query level
    const normalized = query.replace(/\s+/g, " ").trim();

    // Must contain products(
    if (!normalized.includes("products(") && !normalized.includes("products (")) {
      return false;
    }

    // Must NOT contain other root-level Magento queries
    const magentoOnlyQueries = [
      "cart", "customer", "cmsPage", "cmsBlocks", "categories",
      "category", "urlResolver", "storeConfig", "countries",
      "currency", "customAttributeMetadata", "wishlist",
    ];

    for (const q of magentoOnlyQueries) {
      // Check for query field pattern: `queryName(` or `queryName {`
      const pattern = new RegExp(`\\b${q}\\s*[({]`);
      if (pattern.test(normalized)) {
        return false; // Mixed query — let Magento handle it
      }
    }

    return true;
  } catch {
    return false; // Invalid JSON — let Magento handle it
  }
}
```

**Deployment:**

```bash
# Install Wrangler CLI
npm install -g wrangler

# Create wrangler.toml
cat > wrangler.toml <<EOF
name = "magento-graphql-router"
main = "worker.js"
compatibility_date = "2024-01-01"

[vars]
GO_ORIGIN = "https://go-catalog.your-internal-domain.com"
MAGENTO_ORIGIN = "https://magento.your-domain.com"
EOF

# Deploy
wrangler deploy
```

### Nginx Reverse Proxy

For self-hosted setups, Nginx with the `lua-nginx-module` (OpenResty) can inspect the request body and route accordingly.

```nginx
# /etc/nginx/conf.d/graphql-router.conf

upstream magento_backend {
    server 127.0.0.1:443;
    keepalive 32;
}

upstream go_catalog {
    server 127.0.0.1:8080;
    keepalive 32;
}

server {
    listen 443 ssl;
    server_name your-domain.com;

    ssl_certificate     /etc/ssl/certs/your-domain.crt;
    ssl_certificate_key /etc/ssl/private/your-domain.key;

    # Non-GraphQL requests go directly to Magento
    location / {
        proxy_pass https://magento_backend;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }

    # GraphQL endpoint — inspect body to decide backend
    location /graphql {
        # Buffer the request body so we can inspect it
        lua_need_request_body on;

        access_by_lua_block {
            local body = ngx.req.get_body_data()
            if body then
                -- Route to Go if query contains "products(" and no other root queries
                local is_products = string.find(body, '"products%(') or string.find(body, 'products%(')
                       or string.find(body, 'products %(')

                local has_other = string.find(body, '"cart') or string.find(body, '"customer')
                       or string.find(body, '"cmsPage') or string.find(body, '"categories')
                       or string.find(body, '"urlResolver') or string.find(body, '"storeConfig')

                if is_products and not has_other then
                    ngx.var.backend = "go_catalog"
                    return
                end
            end
            ngx.var.backend = "magento_backend"
        }

        set $backend "magento_backend";

        proxy_pass http://$backend;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Store $http_store;
    }
}
```

**Note:** Standard Nginx cannot inspect POST bodies for routing. You need either:
- **OpenResty** (Nginx + LuaJIT) — recommended
- **Nginx with njs** (JavaScript scripting module)
- A lightweight Go/Node proxy in front of both backends

### Lightweight Go Proxy

If you don't want to add Lua to Nginx, a simple Go reverse proxy can handle the routing:

```go
package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

var (
	goBackend      = mustParseURL("http://localhost:8080")
	magentoBackend = mustParseURL("https://magento.your-domain.com")
)

func main() {
	goProxy := httputil.NewSingleHostReverseProxy(goBackend)
	magentoProxy := httputil.NewSingleHostReverseProxy(magentoBackend)

	http.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(strings.NewReader(string(body)))

		if shouldRouteToGo(body) {
			goProxy.ServeHTTP(w, r)
		} else {
			magentoProxy.ServeHTTP(w, r)
		}
	})

	// All other paths go to Magento
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		magentoProxy.ServeHTTP(w, r)
	})

	log.Println("GraphQL router listening on :8000")
	log.Fatal(http.ListenAndServe(":8000", nil))
}

func shouldRouteToGo(body []byte) bool {
	var req struct{ Query string `json:"query"` }
	if json.Unmarshal(body, &req) != nil {
		return false
	}
	q := strings.ToLower(req.Query)
	if !strings.Contains(q, "products(") && !strings.Contains(q, "products (") {
		return false
	}
	// Reject mixed queries containing other Magento root fields
	for _, kw := range []string{"cart", "customer", "cmspage", "categories", "urlresolver", "storeconfig"} {
		if strings.Contains(q, kw) {
			return false
		}
	}
	return true
}

func mustParseURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		log.Fatal(err)
	}
	return u
}
```

### HAProxy

HAProxy can inspect the POST body using `req.body` in ACLs (HAProxy 2.4+):

```haproxy
frontend graphql
    bind *:443 ssl crt /etc/ssl/certs/your-domain.pem
    mode http

    # Inspect POST body for "products(" pattern
    acl is_graphql path /graphql
    acl is_post method POST
    acl is_products_query req.body -m sub products(

    # Route products() to Go, everything else to Magento
    use_backend go_catalog if is_graphql is_post is_products_query
    default_backend magento

backend go_catalog
    mode http
    server go1 127.0.0.1:8080 check

backend magento
    mode http
    server mage1 127.0.0.1:8081 check ssl verify none
```

**Note:** HAProxy body inspection is simpler but less precise — it only checks for substring matches. For mixed queries (e.g., `products()` + `categories()` in a single request), the Cloudflare Worker or Go proxy approach is more reliable.

## Important Routing Considerations

### Store Header

The `Store` HTTP header must be forwarded to the Go service. Both Cloudflare Workers and Nginx examples above preserve all original headers, including `Store`.

### Mixed Queries

Some GraphQL clients may batch multiple root-level queries in a single request (e.g., `{ products(...) { ... } storeConfig { ... } }`). The routing logic above detects these and sends them to Magento, which can handle all query types. If you want the Go service to handle the `products` portion, you would need to split the query — this is not recommended for most setups.

### CORS

The Go service includes CORS middleware that allows all origins. If your proxy terminates CORS at the edge, you can configure the Go service to skip its CORS headers, or let them pass through (browsers use the first `Access-Control-Allow-Origin` header).

### Health Checks

Configure your proxy to health-check the Go service at `/health`. If the Go service is unhealthy, fall back to Magento for `products()` queries:

**Cloudflare Worker with failover:**
```javascript
const response = await fetch(new Request(GO_ORIGIN + "/graphql", { ... }));
if (!response.ok) {
  // Fallback to Magento
  return fetch(new Request(MAGENTO_ORIGIN + "/graphql", { ... }));
}
return response;
```

**Nginx failover:**
```nginx
upstream go_catalog {
    server 127.0.0.1:8080 max_fails=3 fail_timeout=30s;
}

# In the location block, add:
proxy_next_upstream error timeout http_502 http_503;
```

### Cache Invalidation

The Go service has built-in Redis response caching (5-minute TTL). In production, consider:
- Setting a shorter TTL for frequently updated catalogs
- Flushing the Redis cache when products are updated in Magento admin
- Using Magento's webhook/plugin system to trigger cache invalidation

## Deployment Topology Examples

### Small (Single Server)

```
Nginx → ┬─ /graphql (products) → Go :8080
         └─ everything else     → Magento PHP-FPM :9000
```

Both services on the same server, sharing the local MySQL instance.

### Medium (Separate Services)

```
Cloudflare → ┬─ products() → Go (Docker, 1-2 containers)
              └─ rest       → Magento (existing infrastructure)
                                    │
                              MySQL (shared, RDS/CloudSQL)
```

### Large (Kubernetes)

```yaml
# k8s Ingress with annotation-based routing or a Gateway API HTTPRoute
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: graphql-router
spec:
  parentRefs:
    - name: main-gateway
  rules:
    - matches:
        - path:
            type: Exact
            value: /graphql
      # Use an ExtensionRef or middleware to inspect body
      # and route to the appropriate backend
      backendRefs:
        - name: magento-service
          port: 80
```

For Kubernetes, the Cloudflare Worker or a sidecar Go proxy is the most practical routing approach, since Ingress controllers don't natively inspect POST bodies.
