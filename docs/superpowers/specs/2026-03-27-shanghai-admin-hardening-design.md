# Shanghai Admin Hardening Design

**Goal:** Fix the Shanghai production admin authentication path so that brute-force protection uses the real client IP behind Nginx, the tarpit and concurrency lock are verifiable, and deployment includes post-release checks.

## Scope

This design only covers the Shanghai `codex2api` production path:

- real client IP handling for `/api/admin/*`
- admin brute-force protection behavior already being added in `admin/handler.go`
- deployment validation for the Shanghai host

It does not cover Singapore proxy rollout, account import, or password rotation.

## Problem Summary

The current production service is behind Nginx and Docker. `gin.Engine` is running with Gin's default proxy trust settings, which trust all proxies. In that configuration, `Context.ClientIP()` accepts `X-Forwarded-For` from any request path that reaches the app, so brute-force protection can be bypassed by forging a different forwarded IP. At the same time, the tarpit and global validation lock that were added locally were not present in the currently running Shanghai image.

## Approaches Considered

### 1. Keep `ClientIP()` and trust all proxies

This is the current effective behavior. It requires no code changes, but it is not production-safe because forwarded headers remain forgeable.

### 2. Disable forwarded-header trust entirely

Set `SetTrustedProxies(nil)` and use the direct remote address only. This blocks spoofing, but behind host Nginx and Docker all admin requests would collapse to the reverse proxy address, causing one user's failures to ban every admin user behind that proxy path.

### 3. Explicitly configure trusted reverse proxies and keep admin throttling logic separate

Add an explicit `TRUSTED_PROXIES` config entry, apply it to Gin during startup, and extract brute-force state into a small testable component. This preserves real client IPs when the request comes from a trusted reverse proxy and rejects forged forwarded headers otherwise.

**Recommendation:** Approach 3. It is the only option that is both production-safe and operationally usable.

## Design

### Trusted proxy configuration

Add a `TRUSTED_PROXIES` environment variable parsed as a comma-separated list of IPs/CIDRs. During Gin startup:

- if the list is empty, disable forwarded-header trust with `SetTrustedProxies(nil)`
- if the list is present, call `SetTrustedProxies(...)`
- log the effective trusted proxy configuration at startup

For Shanghai production, the trusted proxy list will include the Docker bridge gateway used by the host-to-container hop plus loopback addresses.

### Admin auth guard

Move the mutable brute-force state into a dedicated helper owned by `admin.Handler`. That helper will manage:

- per-IP failure counters
- per-IP temporary bans
- global validation semaphore
- tarpit sleep on failed auth

The middleware will keep using the same user-facing responses, but the state machine will be isolated so it can be tested without the database or full Gin router setup.

### Deployment verification

Shanghai deployment will validate three things after restart:

- container image ID matches the freshly loaded image
- `https://codex.wenrugouai.cn/health` returns `200`
- admin wrong-key behavior matches expectations after release

## Testing Strategy

Two regression layers will be added:

1. Gin trusted-proxy tests proving forwarded headers are ignored for untrusted remotes and honored for trusted remotes.
2. Admin auth-guard tests proving failure counting, temporary blocking, success reset, and tarpit hook invocation.

## Operational Notes

The Shanghai Docker network currently exposes the `codex2api` container on gateway `172.18.0.1`, so production `TRUSTED_PROXIES` should include `172.18.0.1`, `127.0.0.1`, and `::1`.

Subagent-based spec review was not used here because this session does not have user authorization for delegation.
