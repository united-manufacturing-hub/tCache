package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type MetaData struct {
	ETag         string
	LastModified string
}

var (
	cacheMutex  sync.RWMutex
	metaDataMap = make(map[string]MetaData)
)

func main() {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	router := gin.Default()

	router.Use(func(c *gin.Context) {
		cachePath := filepath.Join("cache", strings.ReplaceAll(c.Request.URL.Path, "/", "_"))

		if _, err := os.Stat(cachePath); !os.IsNotExist(err) {
			host := c.Request.URL.Hostname()
			url := fmt.Sprintf("http://%s%s", host, c.Request.URL.Path)

			headResp, err := http.DefaultClient.Head(url)
			if err != nil {
				c.String(http.StatusInternalServerError, "Failed to perform HEAD request")
				return
			}

			etag := headResp.Header.Get("ETag")
			lastModified := headResp.Header.Get("Last-Modified")

			cacheMutex.RLock()
			meta, ok := metaDataMap[cachePath]
			cacheMutex.RUnlock()

			if ok && meta.ETag == etag && meta.LastModified == lastModified {
				logger.Info("Serving from cache", zap.String("path", cachePath))
				c.File(cachePath)
				c.Abort()
			}
		} else {
			c.Next()
		}
	})

	router.Any("/*any", func(c *gin.Context) {
		logger.Info("Proxying request", zap.String("url", c.Request.URL.String()))

		resolver := net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				return net.Dial("udp", "1.1.1.1:53")
			},
		}

		host := c.Request.URL.Hostname()
		ips, err := resolver.LookupIPAddr(c, host)
		if err != nil || len(ips) == 0 {
			c.String(http.StatusInternalServerError, "Failed to resolve domain")
			return
		}

		targetURL := fmt.Sprintf("http://%s%s", ips[0].String(), c.Request.URL.Path)
		resp, err := http.DefaultClient.Get(targetURL)
		if err != nil {
			c.String(http.StatusInternalServerError, "Failed to fetch the resource")
			return
		}
		defer resp.Body.Close()

		content, err := io.ReadAll(resp.Body)
		if err != nil {
			c.String(http.StatusInternalServerError, "Failed to read the content")
			return
		}

		cachePath := filepath.Join("cache", strings.ReplaceAll(c.Request.URL.Path, "/", "_"))
		err = os.WriteFile(cachePath, content, 0644)
		if err != nil {
			c.String(http.StatusInternalServerError, "Failed to write the content to cache")
			return
		}

		meta := MetaData{
			ETag:         resp.Header.Get("ETag"),
			LastModified: resp.Header.Get("Last-Modified"),
		}

		cacheMutex.Lock()
		metaDataMap[cachePath] = meta
		cacheMutex.Unlock()

		logger.Info("Fetched and cached resource", zap.String("path", cachePath))

		c.Data(resp.StatusCode, resp.Header.Get("Content-Type"), content)
	})

	router.Run(":80")
}
