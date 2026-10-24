package api

import (
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	log "github.com/sirupsen/logrus"
)

// typeSafeHeaderPrefix marks TypeSafe request options (e.g. TypeSafe-Danger-Zone-Images) forwarded upstream.
const typeSafeHeaderPrefix = "Typesafe-"

// typeSafeSystemOne forwards a System One judgment request to TypeSafe unchanged, replacing the
// client's proxy key with the configured TypeSafe key (9ooniq).
func (s *Server) typeSafeSystemOne(c *gin.Context) {
	cfg := s.getConfig()
	if cfg == nil || strings.TrimSpace(cfg.TypeSafe.APIKey) == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "TypeSafe System One is not configured"})
		return
	}
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.TypeSafe.BaseURL), "/")
	if baseURL == "" {
		baseURL = config.DefaultTypeSafeBaseURL
	}

	ctx := c.Request.Context()
	upstreamReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v1/systemone", c.Request.Body)
	if errReq != nil {
		log.WithError(errReq).Error("typesafe: failed to build upstream request")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to build TypeSafe request"})
		return
	}
	upstreamReq.ContentLength = c.Request.ContentLength
	for name, values := range c.Request.Header {
		if strings.HasPrefix(http.CanonicalHeaderKey(name), typeSafeHeaderPrefix) {
			upstreamReq.Header[name] = values
		}
	}
	contentType := c.GetHeader("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	upstreamReq.Header.Set("Content-Type", contentType)
	upstreamReq.Header.Set("Authorization", "Bearer "+strings.TrimSpace(cfg.TypeSafe.APIKey))

	resp, errDo := helps.NewProxyAwareHTTPClient(ctx, cfg, nil, 0).Do(upstreamReq)
	if errDo != nil {
		log.WithError(errDo).Warn("typesafe: upstream request failed")
		c.JSON(http.StatusBadGateway, gin.H{"error": "TypeSafe upstream request failed"})
		return
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.WithError(errClose).Debug("typesafe: failed to close upstream response body")
		}
	}()

	if upstreamType := resp.Header.Get("Content-Type"); upstreamType != "" {
		c.Header("Content-Type", upstreamType)
	}
	c.Status(resp.StatusCode)
	if _, errCopy := io.Copy(c.Writer, resp.Body); errCopy != nil {
		log.WithError(errCopy).Warn("typesafe: failed to relay upstream response")
	}
}
