# Shanghai Admin Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Shanghai production admin brute-force protection rely on trusted reverse-proxy IPs, cover the behavior with regression tests, and redeploy with verification.

**Architecture:** Add explicit trusted-proxy configuration at startup and isolate brute-force state into a small admin auth guard. Keep the middleware behavior unchanged externally while making the internal logic deterministic and testable.

**Tech Stack:** Go, Gin v1.12, Docker, PowerShell deployment script, Shanghai Nginx reverse proxy

---

### Task 1: Trusted Proxy Startup Configuration

**Files:**
- Modify: `config/config.go`
- Modify: `.env.example`
- Modify: `main.go`
- Test: `trusted_proxies_test.go`

- [ ] **Step 1: Write the failing test**

Create a router-level test that proves:

- an untrusted remote address ignores `X-Forwarded-For`
- a trusted proxy remote address honors `X-Forwarded-For`

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./... -run TrustedProxies`
Expected: FAIL because startup proxy configuration helper does not exist yet.

- [ ] **Step 3: Write minimal implementation**

Add:

- `TRUSTED_PROXIES` parsing in `config.Load`
- a small startup helper in `main.go` or a new Go file
- startup logging for the effective proxy trust mode

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./... -run TrustedProxies`
Expected: PASS

- [ ] **Step 5: Commit**

Commit message:

```bash
git add config/config.go .env.example main.go trusted_proxies_test.go
git commit -m "fix: configure trusted proxies explicitly"
```

### Task 2: Testable Admin Auth Guard

**Files:**
- Create: `admin/auth_guard.go`
- Create: `admin/auth_guard_test.go`
- Modify: `admin/handler.go`

- [ ] **Step 1: Write the failing test**

Create guard-level tests for:

- block after 5 failures and reject the 6th
- success clears failure state
- failed auth triggers tarpit hook
- acquire timeout returns queue-full response path

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./admin -run AuthGuard`
Expected: FAIL because the guard abstraction does not exist yet.

- [ ] **Step 3: Write minimal implementation**

Extract the brute-force mutable state from `adminAuthMiddleware` into a helper and wire the middleware to use it.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./admin -run AuthGuard`
Expected: PASS

- [ ] **Step 5: Commit**

Commit message:

```bash
git add admin/auth_guard.go admin/auth_guard_test.go admin/handler.go
git commit -m "fix: isolate admin auth throttling"
```

### Task 3: Shanghai Deployment Verification

**Files:**
- Modify: `deploy.ps1`

- [ ] **Step 1: Write the failing test or verification harness**

Because this is deployment scripting, define the post-release checks in the script output and verify manually against Shanghai.

- [ ] **Step 2: Run current deployment verification manually**

Capture the current baseline:

- `https://codex.wenrugouai.cn/health`
- wrong-key responses
- running image ID on Shanghai

- [ ] **Step 3: Write minimal implementation**

Extend `deploy.ps1` so it prints and checks:

- freshly loaded image ID
- container image ID after restart
- remote local health endpoint

- [ ] **Step 4: Run script and verify production**

Run: `./deploy.ps1`
Expected:

- image reload succeeds
- container restarts on Shanghai
- health check succeeds
- wrong-key flow matches new release behavior

- [ ] **Step 5: Commit**

Commit message:

```bash
git add deploy.ps1
git commit -m "chore: verify shanghai deployment"
```

### Task 4: Final Verification

**Files:**
- No additional files

- [ ] **Step 1: Build frontend assets if missing**

Run the existing frontend build so embedded static assets exist before full `go test ./...`.

- [ ] **Step 2: Run repository verification**

Run: `go test ./...`
Expected: PASS

- [ ] **Step 3: Verify production behavior**

Check:

- trusted proxy configuration is loaded on Shanghai
- forged `X-Forwarded-For` no longer bypasses IP blocking
- tarpit and block behavior are visible after release

- [ ] **Step 4: Summarize residual risks**

Document the remaining operational risk that production still depends on a manually managed trusted-proxy env value.
