# OpenAPI Rendering Library Selection

**Decision Date:** 2026-08-09  
**Project:** SEAM Gateway  
**Decision:** Choose **@scalar/api-reference** as the OpenAPI rendering library

## Executive Summary

After evaluating available OpenAPI rendering libraries, **@scalar/api-reference** is selected as the optimal choice for the SEAM Gateway project. This decision balances modern functionality, maintenance status, bundle efficiency, and future extensibility.

## Evaluation Criteria

The selection was based on the following criteria (from the task requirements):

1. **Compatibility** - OpenAPI 3.1 support, ease of integration with Go server
2. **Features** - Interactive capabilities, UI/UX quality, customization options  
3. **Maintenance** - Active development, recent updates, community support
4. **Bundle Size** - Efficient delivery, minimal impact on page load

## Candidates Evaluated

### 1. Redoc (Current Implementation via CDN)
**Status:** ✅ Works well, but ⚠️ CDN dependency limits control

- **Compatibility:** ✅ Excellent OpenAPI 3.1 support
- **Features:** ✅ Good documentation-focused UI, proven in production
- **Maintenance:** ✅ Maintained, but mature/evolving slowly
- **Bundle Size:** ⚠️ Heavy (3+ MB range), CDN-dependent
- **Current Status:** Already implemented via CDN in `/docs` handler
- **Issues:** No npm package control, CDN dependency, heavier bundle

### 2. RapiDoc  
**Status:** ❌ Not recommended due to OpenAPI 3.1 issues

- **Compatibility:** ❌ OpenAPI 3.1 support issues (#1137)
- **Features:** ✅ Good feature set, web components
- **Maintenance:** ❌ Stagnant (last version 9.3.8, 2 years ago)
- **Bundle Size:** ✅ Lightweight (~125KB gzipped)
- **Issues:** OpenAPI 3.1 upload form broken, minimal recent activity

### 3. Swagger UI
**Status:** ⚠️ Good but traditional approach

- **Compatibility:** ✅ Strong OpenAPI 3.1 support
- **Features:** ✅ Comprehensive features, interactive console
- **Maintenance:** ✅ Actively maintained by SmartBear
- **Bundle Size:** ⚠️ Traditionally heavy, multiple packages
- **Issues:** Traditional UI approach, heavier bundles

### 4. Scalar (@scalar/api-reference) ⭐ **RECOMMENDED**
**Status:** ✅ Best overall choice for SEAM

- **Compatibility:** ✅ Full OpenAPI 3.0/3.1 support
- **Features:** ✅ Modern UI, excellent customization, server-side rendering
- **Maintenance:** ✅ Very active (version 1.64.0 published 4 days ago)
- **Bundle Size:** ✅ Optimized bundles, SSR capability
- **Integration:** ✅ Framework-agnostic, easy integration

## Why Scalar?

### Technical Advantages

1. **Modern Architecture**
   - Latest web standards and best practices
   - Server-side rendering support for better performance
   - Framework-agnostic design works well with Go server

2. **Active Maintenance**
   - Recent releases (v1.64.0 published 4 days ago)
   - Active community and development
   - Regular updates and improvements

3. **Feature-Rich**
   - Beautiful, modern UI
   - Built-in interactive API console
   - Advanced theming and customization
   - Multiple integration options (standalone, React, etc.)

4. **Performance**
   - Optimized bundle sizes
   - Server-side rendering capability reduces client-side processing
   - CDN distribution available

5. **Integration Fit**
   - Works perfectly with SEAM's Go server architecture
   - Can be embedded in HTML templates (like current Redoc setup)
   - No framework dependency required

### Migration Path

From current Redoc CDN implementation:

1. **Phase 1:** Install npm package, integrate into existing `/docs` handler
2. **Phase 2:** Configure with SEAM branding and theming
3. **Phase 3:** Migrate existing OpenAPI spec seamlessly
4. **Phase 4:** Enhanced features and customization

### Risk Mitigation

- **Low Risk:** Scalar is mature and widely used
- **Reversible:** Can keep Redoc as fallback during transition
- **Incremental:** Can deploy alongside existing setup
- **Well-Supported:** Active community and documentation

## Decision Matrix

| Criteria | Redoc (Current) | RapiDoc | Swagger UI | Scalar (Winner) |
|----------|----------------|---------|------------|-----------------|
| OpenAPI 3.1 Support | ✅ | ❌ | ✅ | ✅ |
| Maintenance | ⚠️ Mature | ❌ Stagnant | ✅ Active | ✅ Very Active |
| Bundle Size | ❌ Heavy | ✅ Light | ⚠️ Heavy | ✅ Optimized |
| Features | ✅ Good | ✅ Good | ✅ Excellent | ✅ Excellent |
| Modern UI | ⚠️ Traditional | ✅ Modern | ⚠️ Traditional | ✅ Very Modern |
| Integration | ⚠️ CDN-only | ✅ Easy | ✅ Easy | ✅ Flexible |
| Control | ❌ CDN-dep | ✅ npm | ✅ npm | ✅ npm |

## Implementation Plan

1. **Install Scalar as npm dependency**
   ```bash
   npm install @scalar/api-reference
   ```

2. **Integrate into existing Go server**
   - Replace Redoc CDN HTML in `docsHandler`
   - Embed Scalar configuration
   - Preserve existing OpenAPI spec serving

3. **Test integration**
   - Verify `/docs` endpoint loads
   - Confirm OpenAPI spec rendering
   - Test interactive features

4. **Deployment**
   - No breaking changes to API
   - Documentation UI upgrade
   - Improved performance and features

## Sources

- [Scalar vs Redoc vs Swagger UI (2026) — PkgPulse Guides](https://www.pkgpulse.com/guides/scalar-vs-redoc-vs-swagger-ui-api-documentation-2026)
- [The 5 Best API Docs Tools in 2025](https://apisyouwonthate.com/blog/top-5-best-api-docs-tools/)
- [Best Swagger Docs Alternatives 2026](https://www.mintlify.com/library/best-swagger-docs-alternatives)
- [Pros and cons between Swagger UI, Redoc, RapiDoc #141 - GitHub](https://github.com/rapi-doc/RapiDoc/issues/141)
- [The Best API Documentation Tools for Dev Teams](https://bump.sh/blog/the-best-api-documentation-tools-for-dev-teams-2023)
- [@scalar/api-reference - NPM](https://www.npmjs.com/package/@scalar/api-reference)

## Conclusion

**@scalar/api-reference** represents the best choice for SEAM Gateway's OpenAPI rendering needs. It combines modern features, active maintenance, optimal performance, and easy integration with the existing Go server architecture. The migration from the current Redoc CDN implementation is straightforward and low-risk, providing immediate benefits in terms of control, customization, and user experience.

**Next Steps:** Install Scalar and integrate into the existing `/docs` endpoint.