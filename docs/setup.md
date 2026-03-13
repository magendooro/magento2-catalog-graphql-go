# Installation & Setup

## Prerequisites

- **Go 1.23+** (for building from source)
- **MySQL 5.7+** or **MariaDB 10.3+** with an existing Magento 2 database
- **Redis** (optional, for response caching)
- **Docker** (optional, for containerized deployment)

The service requires **read-only access** to a Magento 2 MySQL database. It does not modify any data.

## Installation

### From Source

```bash
git clone https://github.com/magendooro/magento2-catalog-graphql-go.git
cd magento2-catalog-graphql-go
go build -o server ./cmd/server/
```

### Docker

```bash
docker build -t magento2-catalog-graphql .
```

Or use the pre-built image with docker-compose (see [Docker Compose](#docker-compose) below).

## Configuration

The service supports three configuration methods, in order of precedence:

1. **Environment variables** (highest priority)
2. **Config file** (`config/config.yaml`)
3. **Built-in defaults** (lowest priority)

A config file is **not required** — environment variables and defaults are sufficient for most deployments.

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `DB_HOST` | `localhost` | MySQL host |
| `DB_PORT` | `3306` | MySQL port |
| `DB_USER` | `root` | MySQL user |
| `DB_PASSWORD` | `""` | MySQL password |
| `DB_NAME` | `magento` | Magento database name |
| `REDIS_HOST` | `127.0.0.1` | Redis host (leave empty to disable caching) |
| `REDIS_PORT` | `6379` | Redis port |
| `REDIS_PASSWORD` | `""` | Redis password |
| `REDIS_DB` | `0` | Redis database number |
| `SERVER_PORT` / `PORT` | `8080` | HTTP listen port |
| `MEDIA_BASE_URL` | `""` (auto-detect) | Product media base URL; empty = read from Magento's `core_config_data` |
| `LOG_LEVEL` | `info` | Log level: `debug`, `info`, `warn`, `error` |
| `LOG_PRETTY` | `false` | Human-readable log output (disable in production) |

### Config File

Copy the example and edit:

```bash
cp config/config.yaml.example config/config.yaml
```

The file is searched in `./config/config.yaml`, `./config.yaml`, or `/config/config.yaml`.

```yaml
server:
  port: "8080"
  read_timeout: 30s
  write_timeout: 30s

database:
  host: "localhost"
  port: "3306"
  user: "magento_reader"
  password: "secret"
  name: "magento"
  max_open_conns: 25
  max_idle_conns: 10
  conn_max_lifetime: 5m
  conn_max_idle_time: 1m

redis:
  host: "127.0.0.1"
  port: "6379"
  password: ""
  db: 2

graphql:
  complexity_limit: 1000
  max_depth: 15

media:
  base_url: ""  # empty = auto-detect from database

logging:
  level: "info"
  pretty: false
```

### Database User Setup

Create a read-only MySQL user for the service:

```sql
CREATE USER 'magento_reader'@'%' IDENTIFIED BY 'your_secure_password';
GRANT SELECT ON magento.* TO 'magento_reader'@'%';
FLUSH PRIVILEGES;
```

The service only executes `SELECT` queries — it never writes to the database.

### Magento Enterprise Edition (row_id)

This service is built for **Magento Enterprise Edition** (Adobe Commerce), which uses `row_id` instead of `entity_id` in EAV value tables. If you're running Magento Community Edition (Open Source), the EAV JOINs in `internal/repository/product.go` will need to be adjusted to use `entity_id`.

## Running

### Standalone Binary

```bash
# With config file
./server

# With environment variables only
DB_HOST=mysql.example.com DB_NAME=magento DB_PASSWORD=secret ./server

# Override specific settings
DB_HOST=10.0.0.5 LOG_LEVEL=debug ./server
```

### Docker

```bash
docker run -p 8080:8080 \
  -e DB_HOST=host.docker.internal \
  -e DB_NAME=magento \
  -e DB_PASSWORD=secret \
  -e REDIS_HOST=redis.example.com \
  magento2-catalog-graphql
```

To connect to MySQL running on the Docker host machine:
- **macOS/Windows**: Use `host.docker.internal`
- **Linux**: Use `--network host` or the host's IP address

### Docker Compose

The included `docker-compose.yml` starts the service with a Redis sidecar:

```bash
# Configure via .env file or inline
DB_HOST=host.docker.internal DB_NAME=magento docker compose up

# Or create a .env file
cat > .env <<EOF
DB_HOST=host.docker.internal
DB_NAME=magento
DB_USER=magento_reader
DB_PASSWORD=secret
EOF
docker compose up -d
```

### With Config File Volume Mount

```bash
docker run -p 8080:8080 \
  -v $(pwd)/config/config.yaml:/config/config.yaml \
  magento2-catalog-graphql
```

## Endpoints

| Endpoint | Description |
|----------|-------------|
| `POST /graphql` | GraphQL API endpoint |
| `GET /` | GraphQL Playground (development UI) |
| `GET /health` | Health check (pings database) |

## Verifying the Setup

1. **Health check**:
```bash
curl http://localhost:8080/health
# Expected: "ok"
```

2. **Query a product by SKU**:
```bash
curl -s http://localhost:8080/graphql \
  -H 'Content-Type: application/json' \
  -H 'Store: default' \
  -d '{"query":"{ products(filter: { sku: { eq: \"YOUR-SKU\" } }) { items { sku name __typename } total_count } }"}' \
  | python3 -m json.tool
```

3. **Open the GraphQL Playground** at http://localhost:8080/ and explore the schema.

## Multi-Store Setup

Pass the `Store` HTTP header to scope queries to a specific store view:

```bash
curl http://localhost:8080/graphql \
  -H 'Content-Type: application/json' \
  -H 'Store: french' \
  -d '{"query":"{ products(filter: { sku: { eq: \"ABC123\" } }) { items { name } } }"}'
```

The service resolves the store code to a `store_id` and uses it for:
- EAV attribute value scoping (store-specific product names, descriptions, etc.)
- Currency from `currency/options/base`
- URL suffix from `catalog/seo/product_url_suffix`
- Media base URL from `web/secure/base_url`

## Testing

### Integration Tests

Require a running MySQL database with Magento data:

```bash
TEST_DB_NAME=magento TEST_DB_HOST=localhost go test \
  -run 'Test(Products|Bundle|Configurable|Search|Health|Store|Swatch|Only|Empty|Review|Category|Price)' \
  -v -timeout 60s
```

### Comparison Tests

Require both the Go service and a Magento instance running against the **same database**:

```bash
# Start the Go service
./server &

# Run comparison tests
GO_GRAPHQL_URL=http://localhost:8080/graphql \
MAGE_GRAPHQL_URL=http://your-magento.local/graphql \
go test -run TestCompare -v -timeout 300s
```

See [FEATURES_AND_TESTS.md](FEATURES_AND_TESTS.md) for the full test coverage matrix.

## Production Considerations

### Connection Pooling

Tune the database connection pool for your workload:

```yaml
database:
  max_open_conns: 25    # max concurrent MySQL connections
  max_idle_conns: 10    # kept open for reuse
  conn_max_lifetime: 5m # recycle connections after 5 minutes
  conn_max_idle_time: 1m
```

### Redis Caching

Response caching uses a 5-minute TTL with SHA256-hashed cache keys (store code + request body). To disable caching, leave `REDIS_HOST` empty.

### Logging

- Use `LOG_LEVEL=info` in production (default)
- Use `LOG_LEVEL=debug` for troubleshooting
- Set `LOG_PRETTY=false` in production for JSON-structured logs (default)

### Health Checks

The `/health` endpoint pings the MySQL database. Use it for:
- Kubernetes liveness/readiness probes
- Load balancer health checks
- Docker HEALTHCHECK (built into the Dockerfile)
