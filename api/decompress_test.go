package api

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/codex2api/security"
	"github.com/gin-gonic/gin"
)

func TestAutoDecompressMiddlewareGzipBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(AutoDecompressMiddleware(64 << 20))
	r.POST("/v1/responses", func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if got, want := string(body), `{"input":"hello"}`; got != want {
			t.Fatalf("body = %q, want %q", got, want)
		}
		if got := c.Request.Header.Get("Content-Encoding"); got != "" {
			t.Fatalf("Content-Encoding = %q, want empty", got)
		}
		if got := c.Request.ContentLength; got != int64(len(body)) {
			t.Fatalf("ContentLength = %d, want %d", got, len(body))
		}
		if got := mustContextInt64(t, c, BodyCompressedBytesKey); got <= 0 {
			t.Fatalf("compressed bytes = %d, want > 0", got)
		}
		if got := mustContextInt64(t, c, BodyDecompressedBytesKey); got != int64(len(body)) {
			t.Fatalf("decompressed bytes = %d, want %d", got, len(body))
		}
		if got := mustContextInt64(t, c, BodyDecompressMsContextKey); got < 0 {
			t.Fatalf("decompress ms = %d, want >= 0", got)
		}
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", gzipBytes(t, []byte(`{"input":"hello"}`)))
	req.Header.Set("Content-Encoding", "gzip")
	rec := httptest.NewRecorder()

	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestAutoDecompressMiddlewareRejectsInvalidGzip(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(AutoDecompressMiddleware(64 << 20))
	r.POST("/v1/responses", func(c *gin.Context) {
		t.Fatal("handler should not run")
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString("not-gzip"))
	req.Header.Set("Content-Encoding", "gzip")
	rec := httptest.NewRecorder()

	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestAutoDecompressMiddlewareCapsExpandedSizeBeforeRequestSizeLimiter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(AutoDecompressMiddleware(8))
	r.Use(security.RequestSizeLimiter(4))
	r.POST("/v1/responses", func(c *gin.Context) {
		t.Fatal("handler should not run")
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", gzipBytes(t, []byte("123456789")))
	req.Header.Set("Content-Encoding", "gzip")
	rec := httptest.NewRecorder()

	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func gzipBytes(t *testing.T, body []byte) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(body); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return &buf
}

func mustContextInt64(t *testing.T, c *gin.Context, key string) int64 {
	t.Helper()
	v, ok := c.Get(key)
	if !ok {
		t.Fatalf("missing context key %s", key)
	}
	got, ok := v.(int64)
	if !ok {
		t.Fatalf("context key %s = %T, want int64", key, v)
	}
	return got
}
