package api

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	log "github.com/sirupsen/logrus"
)

// maxFishAudioTTSBodyBytes caps a text-to-speech request. Inline voice references travel in the
// MessagePack body, so it is far larger than a JSON speech request: three 4 MiB clips plus text.
const maxFishAudioTTSBodyBytes = 16 << 20

// fishAudioTTS forwards a Fish Audio text-to-speech request unchanged, replacing the client's
// proxy key with the configured Fish Audio key (9ooniq). The body is MessagePack when it carries
// inline voice references, so it is relayed as bytes and never parsed. The `model` header picks
// the Fish Audio model.
func (s *Server) fishAudioTTS(c *gin.Context) {
	cfg := s.getConfig()
	if cfg == nil || strings.TrimSpace(cfg.FishAudio.APIKey) == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "Fish Audio is not configured"})
		return
	}
	if c.Request.ContentLength > maxFishAudioTTSBodyBytes {
		closeUnconsumedRequestOnAbort(c)
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "request body is larger than 16MB"})
		return
	}
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.FishAudio.BaseURL), "/")
	if baseURL == "" {
		baseURL = config.DefaultFishAudioBaseURL
	}

	ctx := c.Request.Context()
	body := http.MaxBytesReader(c.Writer, c.Request.Body, maxFishAudioTTSBodyBytes)
	upstreamReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v1/tts", body)
	if errReq != nil {
		log.WithError(errReq).Error("fishaudio: failed to build upstream request")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to build Fish Audio request"})
		return
	}
	upstreamReq.ContentLength = c.Request.ContentLength
	if model := c.GetHeader("model"); model != "" {
		upstreamReq.Header.Set("model", model)
	}
	contentType := c.GetHeader("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	upstreamReq.Header.Set("Content-Type", contentType)
	upstreamReq.Header.Set("Authorization", "Bearer "+strings.TrimSpace(cfg.FishAudio.APIKey))

	resp, errDo := helps.NewProxyAwareHTTPClient(ctx, cfg, nil, 0).Do(upstreamReq)
	if errDo != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(errDo, &tooLarge) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "request body is larger than 16MB"})
			return
		}
		log.WithError(errDo).Warn("fishaudio: upstream request failed")
		c.JSON(http.StatusBadGateway, gin.H{"error": "Fish Audio upstream request failed"})
		return
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.WithError(errClose).Debug("fishaudio: failed to close upstream response body")
		}
	}()

	if upstreamType := resp.Header.Get("Content-Type"); upstreamType != "" {
		c.Header("Content-Type", upstreamType)
	}
	c.Status(resp.StatusCode)
	if _, errCopy := io.Copy(c.Writer, resp.Body); errCopy != nil {
		log.WithError(errCopy).Warn("fishaudio: failed to relay upstream response")
	}
}
