package main

import (
	"fmt"
	"log"
	"strings"

	"github.com/gin-gonic/gin"
)

func configureTrustedProxies(router *gin.Engine, trustedProxies []string) error {
	if len(trustedProxies) == 0 {
		if err := router.SetTrustedProxies(nil); err != nil {
			return fmt.Errorf("disable trusted proxies: %w", err)
		}
		log.Printf("可信反向代理: disabled (直接使用 RemoteAddr)")
		return nil
	}

	if err := router.SetTrustedProxies(trustedProxies); err != nil {
		return fmt.Errorf("set trusted proxies: %w", err)
	}

	log.Printf("可信反向代理: %s", strings.Join(trustedProxies, ", "))
	return nil
}
