package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestTrustedProxiesIgnoreForwardedHeadersFromUntrustedRemote(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	if err := configureTrustedProxies(router, []string{"127.0.0.1"}); err != nil {
		t.Fatalf("configureTrustedProxies returned error: %v", err)
	}
	router.GET("/ip", func(c *gin.Context) {
		c.String(http.StatusOK, c.ClientIP())
	})

	req := httptest.NewRequest(http.MethodGet, "/ip", nil)
	req.RemoteAddr = "203.0.113.10:3456"
	req.Header.Set("X-Forwarded-For", "198.51.100.77")

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "203.0.113.10" {
		t.Fatalf("expected direct remote IP, got %q", got)
	}
}

func TestTrustedProxiesHonorForwardedHeadersFromTrustedRemote(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	if err := configureTrustedProxies(router, []string{"10.0.0.0/8"}); err != nil {
		t.Fatalf("configureTrustedProxies returned error: %v", err)
	}
	router.GET("/ip", func(c *gin.Context) {
		c.String(http.StatusOK, c.ClientIP())
	})

	req := httptest.NewRequest(http.MethodGet, "/ip", nil)
	req.RemoteAddr = "10.1.2.3:4567"
	req.Header.Set("X-Forwarded-For", "198.51.100.88")

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "198.51.100.88" {
		t.Fatalf("expected forwarded client IP, got %q", got)
	}
}
