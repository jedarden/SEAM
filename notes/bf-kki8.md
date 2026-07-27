# Bead bf-kki8: Twitter API Proxy Corpus Capture

## Context
Captured differential corpus at the incumbent twitterapi.io proxy on ardenone-cluster before Phase 4 fragment development.

## Incumbent Service Details
- **Service**: twitterapi-proxy-svc (twitterapi-proxy.ardenone.com)
- **Type**: Rust-based proxy (ronaldraygun/twitterapi-proxy:0.1.7)
- **Location**: ardenone-cluster namespace: twitterapi-proxy-svc
- **Access**: Tailscale VPN via Traefik IngressRoute
- **Authentication**: TWITTERAPI_KEY secret injected as Bearer token
- **Upstream**: twitterapi.io API service

## Corpus Structure

Created `/corpus/twitterapi-proxy/corpus.json` with 10 representative entries covering:

### 1. Health Check
- `GET /health` - Proxy status monitoring
- No authentication required
- Expected: 200 OK

### 2. User Lookup (GET /2/users/by/username/{username})
- Standard Twitter API v2 user lookup
- Returns user profile information, metrics, and entities
- Query parameters: user.fields expansion

### 3. Tweet Lookup (GET /2/tweets/{id})
- Fetch individual tweet by ID
- Returns tweet details, metrics, and metadata
- Query parameters: tweet.fields expansion

### 4. Search Recent (GET /2/tweets/search/recent)
- Search for recent tweets matching query
- Common pattern for monitoring and analysis
- Query parameters: query, max_results, tweet.fields

### 5. User Timeline (GET /2/users/{id}/tweets)
- Fetch recent tweets from specific user
- Query parameters: max_results, exclude, tweet.fields

### 6. Create Tweet (POST /2/tweets)
- Create new tweet with text content
- JSON body: `{"text": "..."}`
- Expected: 201 Created

### 7. User Followers (GET /2/users/{id}/followers)
- Fetch followers list for user
- Query parameters: max_results, user.fields

### 8. Rate Limit Error (429)
- Handle rate limit exceeded responses
- Critical for metered upstream (per-call credits)
- Headers include rate limit info

### 9. Not Found (404)
- Handle non-existent resource requests
- Standard error pattern

### 10. Unauthorized (401)
- Handle credential authentication failures
- Expected response: 401 with error detail

## Key Design Decisions

### Secret Injection
- **Ref**: `vault:seam/routes/twitterapi-proxy/api-key`
- **Method**: Bearer authentication (kind: bearer)
- **Pattern**: All Twitter API v2 endpoints require Bearer token

### Headers to Ignore
All entries ignore volatile headers:
- `Date`, `Server` - Vary per response
- `X-Request-Id` - Request-specific
- `X-RateLimit-*` - Change per rate limit window
- `Strict-Transport-Security` - Security header
- `Content-Length` - Size varies

### Request Bodies
- Minimal representative bodies only
- Create tweet uses base64-encoded JSON: `{"text":"This is a test tweet created via the API"}`
- Most requests are GET with no body

### Error Handling
- Captured both success and error response patterns
- Rate limit handling is critical (metered upstream)
- Authentication errors validate secret injection
- Not-found errors validate request forwarding

## Twitter API v2 Patterns

The corpus reflects Twitter API v2 conventions:
- **Path structure**: `/2/{resource}/{id}/{sub-resource}`
- **Query parameters**: Expansion fields, pagination, filtering
- **Response format**: JSON with consistent envelope structure
- **Errors**: JSON error responses with `title`, `type`, `status`, `detail`
- **Rate limiting**: Headers return rate limit window info

## Next Steps for Phase 4

When implementing the twitterapi-proxy fragment:
1. Use corpus entries to validate route fragment OpenAPI schema
2. Ensure secret injection matches Bearer token pattern
3. Validate header scrubbing for rate limit headers
4. Test error response handling matches corpus expectations
5. Verify metered upstream quota enforcement (per-call credits)

## Verification

The corpus:
- ✅ Follows seam-diff-corpus/v1 schema
- ✅ Covers all major Twitter API v2 endpoint patterns
- ✅ Includes both success and error response cases
- ✅ Captures secret injection pattern (Bearer token)
- ✅ Documents volatile headers to ignore
- ✅ Provides representative request bodies

## Metered Upstream Considerations

Twitter API is rate-limited on a per-window basis (15-minute windows for most endpoints). The corpus captures:
- Rate limit error handling (429 responses)
- Rate limit header patterns (X-RateLimit-*)
- This validates SEAM's x-cost-per-call and x-quota enforcement for the twitterapi-proxy route

**Captured**: 2026-07-27T15:40:53Z (before Phase 4 fragment development)