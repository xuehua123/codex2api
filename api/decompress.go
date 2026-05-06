package api

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// AutoDecompressMiddleware expands supported compressed request bodies before
// downstream middleware enforces the normal uncompressed body size limit.
func AutoDecompressMiddleware(maxDecompressedBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Body == nil || c.Request.Body == http.NoBody {
			c.Next()
			return
		}
		if !requestHasContentEncoding(c.Request, "gzip") {
			c.Next()
			return
		}
		if maxDecompressedBytes <= 0 {
			setDecompressMetrics(c, 0, maxInt64(c.Request.ContentLength, 0), 0)
			c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{
				"error": gin.H{
					"message": "请求体过大",
					"type":    "invalid_request_error",
					"code":    "request_too_large",
				},
			})
			return
		}
		if c.Request.ContentLength > maxDecompressedBytes {
			setDecompressMetrics(c, 0, c.Request.ContentLength, 0)
			c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{
				"error": gin.H{
					"message": "压缩请求体过大",
					"type":    "invalid_request_error",
					"code":    "request_too_large",
				},
			})
			return
		}

		originalBody := c.Request.Body
		counter := &countingReadCloser{ReadCloser: originalBody}
		decompressStart := time.Now()
		gzipReader, err := gzip.NewReader(counter)
		if err != nil {
			setDecompressMetrics(c, time.Since(decompressStart), counter.BytesRead(), 0)
			_ = counter.Close()
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"error": gin.H{
					"message": "gzip 请求体无效",
					"type":    "invalid_request_error",
					"code":    "invalid_request_body",
				},
			})
			return
		}

		decompressed, readErr := io.ReadAll(io.LimitReader(gzipReader, maxDecompressedBytes+1))
		closeErr := gzipReader.Close()
		decompressDuration := time.Since(decompressStart)
		_ = counter.Close()
		setDecompressMetrics(c, decompressDuration, counter.BytesRead(), int64(len(decompressed)))
		if readErr != nil || closeErr != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"error": gin.H{
					"message": "gzip 请求体解压失败",
					"type":    "invalid_request_error",
					"code":    "invalid_request_body",
				},
			})
			return
		}
		if int64(len(decompressed)) > maxDecompressedBytes {
			c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{
				"error": gin.H{
					"message": "请求体过大",
					"type":    "invalid_request_error",
					"code":    "request_too_large",
				},
			})
			return
		}

		c.Request.Body = io.NopCloser(bytes.NewReader(decompressed))
		c.Request.ContentLength = int64(len(decompressed))
		c.Request.Header.Del("Content-Encoding")
		c.Request.Header.Del("Content-Length")
		c.Next()
	}
}

type countingReadCloser struct {
	io.ReadCloser
	bytesRead int64
}

func (r *countingReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	r.bytesRead += int64(n)
	return n, err
}

func (r *countingReadCloser) BytesRead() int64 {
	if r == nil {
		return 0
	}
	return r.bytesRead
}

func setDecompressMetrics(c *gin.Context, duration time.Duration, compressedBytes, decompressedBytes int64) {
	if c == nil {
		return
	}
	c.Set(BodyDecompressMsContextKey, duration.Milliseconds())
	c.Set(BodyCompressedBytesKey, compressedBytes)
	c.Set(BodyDecompressedBytesKey, decompressedBytes)
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func requestHasContentEncoding(req *http.Request, expected string) bool {
	for _, value := range req.Header.Values("Content-Encoding") {
		for _, part := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), expected) {
				return true
			}
		}
	}
	return false
}
