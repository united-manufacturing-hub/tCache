package main

import (
	"context"
	"fmt"
	"golang.org/x/crypto/sha3"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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

func getCachePath(url *url.URL) string {
	hasher := sha3.New256()
	hasher.Write([]byte(url.String()))
	hash := fmt.Sprintf("%x", hasher.Sum(nil))
	return filepath.Join("cache", hash)
}

func main() {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	router := gin.Default()

	router.Use(func(c *gin.Context) {
		cachePath := getCachePath(c.Request.URL)
		if _, err := os.Stat(cachePath); !os.IsNotExist(err) {
			host := c.Request.Host
			// If hostname is empty, check the Host header
			if host == "" {
				logger.Info("Hostname is empty, checking Host header")
				host = c.Request.Header.Get("Host")
			}
			resolver := net.Resolver{
				PreferGo: true,
				Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
					return net.Dial("udp", "1.1.1.1:53")
				},
			}
			ips, err := resolver.LookupIPAddr(c, host)
			if err != nil || len(ips) == 0 {
				logger.Info("Failed to resolve domain", zap.String("domain", host))
				c.String(http.StatusInternalServerError, "Failed to resolve domain")
				return
			}
			targetURL := fmt.Sprintf("http://%s%s", ips[0].String(), c.Request.URL.Path)

			logger.Info("Performing HEAD request", zap.String("url", targetURL))

			// Do http request but set Host header
			req, err := http.NewRequest(http.MethodHead, targetURL, c.Request.Body)
			if err != nil {
				c.String(http.StatusInternalServerError, "Failed to create request")
				return
			}
			req.Header.Set("Host", host)
			headResp, err := http.DefaultClient.Do(req)
			if err != nil {
				logger.Info("Failed to perform HEAD request", zap.String("error", err.Error()))
				c.String(http.StatusInternalServerError, "Failed to perform HEAD request")
				c.Abort()
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
			} else {
				logger.Info("Cache miss", zap.String("path", cachePath))
				logger.Info("Etag value mismatch", zap.String("cached", meta.ETag), zap.String("remote", etag))
				logger.Info("Last-Modified value mismatch", zap.String("cached", meta.LastModified), zap.String("remote", lastModified))
				logger.Info("Is ok?", zap.Bool("ok", ok))
				c.Next()
			}
		} else {
			logger.Info("Cache miss", zap.String("path", cachePath))
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

		host := c.Request.Host
		// If hostname is empty, check the Host header
		if host == "" {
			logger.Info("Hostname is empty, checking Host header")
			host = c.Request.Header.Get("Host")
		}
		ips, err := resolver.LookupIPAddr(c, host)
		if err != nil || len(ips) == 0 {
			logger.Info("Failed to resolve domain", zap.String("domain", host))
			c.String(http.StatusInternalServerError, "Failed to resolve domain")
			return
		}

		targetURL := fmt.Sprintf("http://%s%s", ips[0].String(), c.Request.URL.Path)
		logger.Info("Fetching resource", zap.String("url", targetURL))

		// Do http request but set Host header
		req, err := http.NewRequest(c.Request.Method, targetURL, c.Request.Body)
		if err != nil {
			c.String(http.StatusInternalServerError, "Failed to create request")
			return
		}
		req.Header.Set("Host", host)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			c.String(http.StatusInternalServerError, "Failed to fetch the resource")
			return
		}
		defer resp.Body.Close()

		cachePath := getCachePath(c.Request.URL)
		// Create folder if it doesn't exist
		if _, err := os.Stat("cache"); os.IsNotExist(err) {
			os.Mkdir("cache", 0755)
		}
		cacheFile, err := os.Create(cachePath)
		if err != nil {
			c.String(http.StatusInternalServerError, "Failed to create cache file")
			return
		}
		defer cacheFile.Close()

		teeReader := io.TeeReader(resp.Body, cacheFile)
		c.DataFromReader(resp.StatusCode, resp.ContentLength, resp.Header.Get("Content-Type"), teeReader, nil)

		meta := MetaData{
			ETag:         resp.Header.Get("ETag"),
			LastModified: resp.Header.Get("Last-Modified"),
		}

		cacheMutex.Lock()
		metaDataMap[cachePath] = meta
		logger.Info("Cached resource", zap.String("path", cachePath))
		logger.Info("Cached resource [ETag]", zap.String("etag", meta.ETag))
		logger.Info("Cached resource [LM]", zap.String("last-modified", meta.LastModified))
		cacheMutex.Unlock()

		logger.Info("Fetched and cached resource", zap.String("path", cachePath))

	})

	router.Run(":80")
}
