# OpenAPI Documentation Rendering Libraries - Comparison Matrix

**Project:** SEAM (github.com/ardenone/seam)  
**Go Version:** 1.25.7  
**Evaluation Date:** 2025-08-07

## Quick Reference Matrix

| Library | Version | OpenAPI 3.1 | Maintenance | License | Integration | Static Assets | Recommendation |
|---------|---------|-------------|-------------|---------|-------------|---------------|----------------|
| **pb33f/libopenapi** | v0.38.7 | ✅ Full | ✅ Active | Apache-2.0 | 🟡 Medium | ✅ Yes | ⭐ **RECOMMENDED** |
| **mvrilo/go-redoc** | v0.1.5 | ⚠️ Indirect | ⚠️ Questionable | MIT | ✅ Easy | ✅ Yes | 🟡 Current choice |
| **getkin/kin-openapi** | v0.146.0 | ✅ Full | ✅ Active | Apache-2.0 | ✅ Easy | ❌ Parser only | ⚠️ Parser, not renderer |
| **oaswrap/openapi-ui** | v1.0.0 | ❓ Unknown | ❓ Unknown | ❓ Unknown | ❓ Unknown | ❓ Unknown | ❌ Needs investigation |
| **go-swagger** | v0.36.2 | ❌ No | ⚠️ Maint. mode | Apache-2.0 | ✅ Easy | ✅ Yes | ❌ **NOT RECOMMENDED** |

## Detailed Analysis

### pb33f/libopenapi ⭐ RECOMMENDED

**Status:** ✅ **FULLY COMPATIBLE** - Already in SEAM dependencies

**Capabilities:**
- OpenAPI 3.0, 3.1, and 3.2 support
- High-performance parser
- Documentation rendering via `/renderer` package
- Built-in validation (libopenapi-validator)
- Strong typing with Go structs
- Comprehensive documentation at pb33f.io

**Integration Level:** Medium complexity, high capability

**Maintenance:** Very active (92+ releases, latest v0.38.7)

**Best For:** Production applications requiring full OpenAPI 3.1 support

---

### mvrilo/go-redoc 🟡 CURRENT CHOICE

**Status:** ⚠️ **USE WITH CAUTION** - Currently integrated in SEAM

**Capabilities:**
- Embedded ReDoc UI using Go embed
- OpenAPI 3.1 support via bundled ReDoc
- Simple middleware integration
- Works with net/http, gin, fiber

**Integration Level:** Very easy

**Maintenance:** Questionable (limited recent activity)

**Best For:** Simple UI rendering, low-complexity projects

**Note:** Already integrated in SEAM at `/docs` endpoint

---

### getkin/kin-openapi ⚠️ PARSER ONLY

**Status:** ✅ **COMPATIBLE** - Already in SEAM dependencies

**Capabilities:**
- OpenAPI 3.0, 3.1, and Swagger v2 parsing
- Strong community adoption
- Well-documented

**Integration Level:** Easy

**Maintenance:** Active

**Best For:** Parsing and validation, NOT rendering UI

**Note:** Complementary to rendering libraries

---

### go-swagger ❌ NOT RECOMMENDED

**Status:** ❌ **INCOMPATIBLE** - No OpenAPI 3.1 support

**Capabilities:**
- OpenAPI 2.0 (Swagger) ONLY
- No OpenAPI 3.x support

**Maintenance:** Maintenance mode only

**Best For:** Legacy OpenAPI 2.0 projects

**Dealbreaker:** No OpenAPI 3.1 support

---

## Import Compatibility Test Results

✅ **ALL LIBRARIES IMPORT SUCCESSFULLY**

```bash
$ go run test_openapi_imports_clean.go
✅ All OpenAPI libraries imported successfully!
Go version compatibility: 1.25.7
Tested libraries:
  - getkin/kin-openapi (OpenAPI 3.1 parser)
  - go-openapi/runtime/server-middleware (Swagger UI)
  - mvrilo/go-redoc (ReDoc UI)
  - oaswrap/openapi-ui (Alternative UI)
  - pb33f/libopenapi (Comprehensive OpenAPI toolkit)
```

## Recommendation Summary

### Primary Recommendation: pb33f/libopenapi

**Why:**
- ✅ Full OpenAPI 3.1 support
- ✅ Active development
- ✅ Already in dependencies
- ✅ Production-ready
- ✅ Comprehensive documentation
- ✅ Built-in validation

### Secondary: Continue go-redoc

**Why:**
- ✅ Already integrated
- ✅ Simple to use
- ⚠️ Monitor maintenance

### Not Recommended: go-swagger

**Why:**
- ❌ No OpenAPI 3.1 support
- ⚠️ Maintenance mode only

## Sources

- [pb33f/libopenapi](https://github.com/pb33f/libopenapi)
- [pb33f Documentation](https://pb33f.io)
- [go-redoc](https://github.com/mvrilo/go-redoc)
- [getkin/kin-openapi](https://github.com/getkin/kin-openapi)
- [go-swagger](https://github.com/go-swagger/go-swagger)
