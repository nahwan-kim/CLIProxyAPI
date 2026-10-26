package live

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

const (
	// transcriptionSessionModel hosts the Realtime session; with text-only output and no
	// response.create it never answers, so the transcript comes only from the transcription model.
	transcriptionSessionModel = "gpt-realtime-2.1"
	// defaultTranscriptionModel is used when the request names no model.
	defaultTranscriptionModel = "gpt-live-transcribe"
	// maxTranscriptionAudioBytes matches the public /v1/audio/transcriptions upload limit.
	maxTranscriptionAudioBytes = 25 << 20
	transcriptionFormSlack     = 1 << 20
	transcriptionSampleRate    = 24000
	// transcriptionChunkBytes is one second of 24 kHz mono PCM16 per input_audio_buffer.append.
	transcriptionChunkBytes = transcriptionSampleRate * 2
	// transcriptionMinSpeechBytes is the shortest buffer the Realtime API commits (100 ms).
	transcriptionMinSpeechBytes = transcriptionSampleRate * 2 / 10
	// transcriptionFormats are the demuxers ffmpeg may pick for an upload: recordings only, never
	// playlists (concat, HLS) that would read other files or URLs.
	transcriptionFormats = "mov,mp4,m4a,matroska,webm,wav,ogg,mp3,aac,flac"
	// maxClientErrorChars bounds upstream error text passed on to the client.
	maxClientErrorChars = 300
)

// maxTranscriptionSeconds caps the decoded recording: a Realtime session lasts at most 60 minutes,
// and decoding is bounded so a small compressed upload cannot expand without limit. Tests lower it.
var maxTranscriptionSeconds = 30 * 60

// decodeTranscriptionAudio turns an uploaded recording into 24 kHz mono PCM16; tests replace it.
var decodeTranscriptionAudio = decodeWithFFmpeg

var errAudioTooLong = errors.New("audio too long")

type transcriptionRequest struct {
	audio     []byte
	model     string
	prompt    string
	keywords  []string
	languages []string
	stream    bool
}

type transcriptionRequestError struct{ message string }

func (e transcriptionRequestError) Error() string { return e.message }

// upstreamEventError carries an error the Realtime API reported in an event; its message is
// meant for the client. settings marks a rejected session.update: the request's model, prompt,
// keywords or languages, so a client error.
type upstreamEventError struct {
	message  string
	settings bool
}

func (e upstreamEventError) Error() string { return e.message }

// HandleAudioTranscription serves POST /v1/audio/transcriptions over a Codex OAuth Realtime
// session: the recording is decoded, sent as one committed turn, and the transcription model's
// result is answered in the public API's shape (JSON, or SSE when stream=true) (9ooniq).
func (h *Handler) HandleAudioTranscription(c *gin.Context) {
	if h == nil || h.authManager == nil {
		writeRealtimeError(c, http.StatusServiceUnavailable, "Codex auth manager unavailable", "server_error", "codex_auth_unavailable")
		return
	}
	request, errParse := parseTranscriptionRequest(c)
	if errParse != nil {
		status := http.StatusBadRequest
		var tooLarge *http.MaxBytesError
		if errors.As(errParse, &tooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		writeRealtimeError(c, status, errParse.Error(), "invalid_request_error", "invalid_transcription_request")
		return
	}
	ctx := c.Request.Context()
	pcm, errDecode := decodeTranscriptionAudio(ctx, request.audio)
	if errors.Is(errDecode, errAudioTooLong) {
		writeRealtimeError(c, http.StatusBadRequest, fmt.Sprintf("Audio is longer than %d seconds", maxTranscriptionSeconds), "invalid_request_error", "audio_too_long")
		return
	}
	if errDecode != nil {
		log.Warnf("codex transcription: decode audio failed: %s", logging.SafeDiagnosticForLog(errDecode.Error()))
		writeRealtimeError(c, http.StatusBadRequest, "Could not decode the audio file", "invalid_request_error", "invalid_audio")
		return
	}
	if len(pcm) < transcriptionMinSpeechBytes {
		writeRealtimeError(c, http.StatusBadRequest, "Audio file is too short", "invalid_request_error", "audio_too_short")
		return
	}

	upstream, end, ok := h.dialTranscriptionSession(c)
	if !ok {
		return
	}
	defer end()
	stop := context.AfterFunc(ctx, func() { _ = upstream.Close() })
	defer stop()

	sse := newTranscriptionSSE(c, request.stream)
	text, errRun := runTranscription(upstream, request, pcm, sse.delta)
	if errRun != nil {
		if ctx.Err() != nil {
			return
		}
		log.Warnf("codex transcription: realtime session failed: %s", logging.SafeDiagnosticForLog(errRun.Error()))
		status, errorType, code := http.StatusBadGateway, "api_error", "transcription_failed"
		message := "Realtime transcription session ended unexpectedly"
		var eventErr upstreamEventError
		if errors.As(errRun, &eventErr) {
			message = eventErr.message
			if eventErr.settings {
				status, errorType, code = http.StatusBadRequest, "invalid_request_error", "invalid_transcription_settings"
			}
		}
		if sse.started {
			sse.fail(message)
			return
		}
		writeRealtimeError(c, status, message, errorType, code)
		return
	}
	if request.stream {
		sse.done(text)
		return
	}
	c.JSON(http.StatusOK, gin.H{"text": text})
}

func parseTranscriptionRequest(c *gin.Context) (transcriptionRequest, error) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxTranscriptionAudioBytes+transcriptionFormSlack)
	if errForm := c.Request.ParseMultipartForm(maxTranscriptionAudioBytes + transcriptionFormSlack); errForm != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(errForm, &tooLarge) {
			return transcriptionRequest{}, errForm
		}
		return transcriptionRequest{}, transcriptionRequestError{"Send multipart/form-data with an audio file"}
	}
	header, errFile := c.FormFile("file")
	if errFile != nil {
		return transcriptionRequest{}, transcriptionRequestError{"Missing audio file"}
	}
	if header.Size > maxTranscriptionAudioBytes {
		return transcriptionRequest{}, transcriptionRequestError{fmt.Sprintf("Audio file exceeds %d bytes", maxTranscriptionAudioBytes)}
	}
	file, errOpen := header.Open()
	if errOpen != nil {
		return transcriptionRequest{}, transcriptionRequestError{"Could not read the audio file"}
	}
	defer func() { _ = file.Close() }()
	audio, errRead := io.ReadAll(file)
	if errRead != nil {
		return transcriptionRequest{}, transcriptionRequestError{"Could not read the audio file"}
	}
	if len(audio) == 0 {
		return transcriptionRequest{}, transcriptionRequestError{"Audio file is empty"}
	}
	model := strings.TrimSpace(c.PostForm("model"))
	if model == "" {
		model = defaultTranscriptionModel
	}
	languages := formValues(c, "languages[]", "languages")
	if language := strings.TrimSpace(c.PostForm("language")); language != "" && len(languages) == 0 {
		languages = []string{language}
	}
	return transcriptionRequest{
		audio:     audio,
		model:     model,
		prompt:    strings.TrimSpace(c.PostForm("prompt")),
		keywords:  formValues(c, "keywords[]", "keywords"),
		languages: languages,
		stream:    strings.EqualFold(strings.TrimSpace(c.PostForm("stream")), "true"),
	}, nil
}

func formValues(c *gin.Context, names ...string) []string {
	var values []string
	for _, name := range names {
		for _, value := range c.PostFormArray(name) {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				values = append(values, trimmed)
			}
		}
	}
	return values
}

// decodeWithFFmpeg goes through a temporary file: MP4/M4A from Safari keeps its index at the end,
// which ffmpeg cannot seek to on a pipe.
func decodeWithFFmpeg(ctx context.Context, audio []byte) ([]byte, error) {
	ffmpeg, errLook := exec.LookPath("ffmpeg")
	if errLook != nil {
		return nil, fmt.Errorf("ffmpeg not found: %w", errLook)
	}
	input, errTemp := os.CreateTemp("", "cliproxy-transcription-*")
	if errTemp != nil {
		return nil, fmt.Errorf("create temp audio: %w", errTemp)
	}
	defer func() {
		if errRemove := os.Remove(input.Name()); errRemove != nil {
			log.WithError(errRemove).Debug("codex transcription: remove temp audio failed")
		}
	}()
	if _, errWrite := input.Write(audio); errWrite != nil {
		_ = input.Close()
		return nil, fmt.Errorf("write temp audio: %w", errWrite)
	}
	if errClose := input.Close(); errClose != nil {
		return nil, fmt.Errorf("close temp audio: %w", errClose)
	}
	stdout := &limitedBuffer{max: (maxTranscriptionSeconds + 1) * transcriptionSampleRate * 2}
	stderr := &limitedBuffer{max: 2048}
	cmd := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-nostdin",
		"-format_whitelist", transcriptionFormats, "-protocol_whitelist", "file",
		"-i", input.Name(), "-t", fmt.Sprint(maxTranscriptionSeconds+1),
		"-vn", "-f", "s16le", "-acodec", "pcm_s16le", "-ac", "1", "-ar", fmt.Sprint(transcriptionSampleRate), "pipe:1")
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if errRun := cmd.Run(); errRun != nil {
		return nil, fmt.Errorf("ffmpeg: %w: %s", errRun, strings.TrimSpace(stderr.String()))
	}
	if stdout.Len() > maxTranscriptionSeconds*transcriptionSampleRate*2 {
		return nil, errAudioTooLong
	}
	return stdout.Bytes(), nil
}

// limitedBuffer keeps at most max bytes and drops the rest, so a process's output cannot grow
// without bound; ffmpeg's -t keeps the decoded audio within it.
type limitedBuffer struct {
	bytes.Buffer
	max int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.Len(); room > 0 {
		_, _ = b.Buffer.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

// dialTranscriptionSession opens the upstream Realtime WebSocket with a scheduled Codex OAuth
// credential. On failure it has already answered the client.
func (h *Handler) dialTranscriptionSession(c *gin.Context) (*websocket.Conn, func(), bool) {
	ctx := context.WithValue(c.Request.Context(), "gin", c)
	selectionModel := codexRealtimeModel(transcriptionSessionModel)
	selectionOpts := coreexecutor.Options{Headers: liveSelectionHeaders(c)}
	ctx = handlers.EnrichContextWithSessionHierarchy(ctx, selectionOpts.Headers, nil, nil)
	selection, selected, errSelect := h.selectOAuth(ctx, selectionModel, selectionOpts)
	if errSelect != nil {
		writeSelectionError(c, errSelect)
		return nil, nil, false
	}
	if selected == nil {
		if selection != nil {
			selection.End("missing_auth")
		}
		writeRealtimeError(c, http.StatusServiceUnavailable, "Codex auth unavailable", "server_error", "codex_auth_unavailable")
		return nil, nil, false
	}
	end := func() {}
	if selection != nil {
		attemptCtx, releaseAttempt, errAttempt := selection.AttemptContext(ctx)
		if errAttempt != nil {
			selection.End("attempt_bind_failed")
			writeRealtimeError(c, http.StatusServiceUnavailable, errAttempt.Error(), "server_error", "realtime_upstream_unavailable")
			return nil, nil, false
		}
		ctx = attemptCtx
		selection.Retain()
		end = func() {
			releaseAttempt()
			selection.End("session_closed")
		}
	}
	logging.SetGinCPATraceID(c, selected.EnsureIndex())

	upstreamURL := h.directRealtimeURL(transcriptionSessionModel)
	request, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, websocketHTTPURL(upstreamURL), nil)
	if errRequest != nil {
		end()
		writeRealtimeError(c, http.StatusInternalServerError, "Failed to build Realtime request", "server_error", "realtime_request_failed")
		return nil, nil, false
	}
	request.Header = directRealtimeHeaders(make(http.Header))
	setAccountHeader(request.Header, selected)
	if errPrepare := h.authManager.PrepareHttpRequest(ctx, selected, request); errPrepare != nil {
		end()
		writeRealtimeError(c, http.StatusServiceUnavailable, "Codex auth unavailable", "server_error", "codex_auth_unavailable")
		return nil, nil, false
	}
	authType, authValue := selected.AccountInfo()
	helpersConfig := h.currentConfig()
	helps.RecordAPIWebsocketRequest(ctx, helpersConfig, helps.UpstreamRequestLog{
		URL:       upstreamURL,
		Method:    "WEBSOCKET",
		Headers:   headersForLogging(request.Header),
		Provider:  "codex",
		AuthID:    selected.ID,
		AuthLabel: selected.Label,
		AuthType:  authType,
		AuthValue: authValue,
	})
	upstream, response, errDial := newProxyAwareSidebandDialer(helpersConfig, selected).DialContext(ctx, upstreamURL, request.Header)
	if errDial != nil {
		status := http.StatusBadGateway
		var body []byte
		if response != nil && response.StatusCode > 0 {
			status = response.StatusCode
			copyRealtimeHandshakeHeaders(c.Writer.Header(), response.Header)
			if response.Body != nil {
				body, _ = readLimitedBody(response.Body)
			}
		}
		closeHandshakeBody(response, "transcription websocket rejected")
		helps.RecordAPIWebsocketError(ctx, helpersConfig, "dial", errDial)
		if selection != nil && status == http.StatusUnauthorized {
			if len(body) == 0 {
				body = []byte(errDial.Error())
			}
			h.authManager.ReportHomeUnauthorized(ctx, selected, "codex", selectionModel, body)
		}
		end()
		log.WithField("status", status).Warnf("codex transcription: realtime handshake failed: %s", logging.SafeDiagnosticForLog(string(body)))
		switch status {
		case http.StatusUnauthorized, http.StatusForbidden:
			writeRealtimeError(c, status, "Codex Realtime upstream rejected the credential", "authentication_error", "realtime_upstream_unauthorized")
		case http.StatusTooManyRequests:
			writeRealtimeError(c, status, "Codex Realtime upstream is rate limited", "rate_limit_error", "realtime_upstream_rate_limited")
		default:
			writeRealtimeError(c, http.StatusBadGateway, "Codex Realtime upstream unavailable", "api_error", "realtime_upstream_unavailable")
		}
		return nil, nil, false
	}
	closeHandshakeBody(response, "transcription websocket handshake")
	closeUpstream := websocketCloseFunc("upstream", upstream)
	cleanup := func() {
		_ = closeUpstream()
		end()
	}
	if selection != nil {
		if errBind := selection.Bind(closeUpstream); errBind != nil {
			cleanup()
			writeRealtimeError(c, http.StatusServiceUnavailable, errBind.Error(), "server_error", "realtime_upstream_unavailable")
			return nil, nil, false
		}
	}
	return upstream, cleanup, true
}

type realtimeEvent struct {
	Type       string `json:"type"`
	Delta      string `json:"delta"`
	Transcript string `json:"transcript"`
	Error      *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// errorMessage is the client-facing text of an error event, bounded in length.
func (e realtimeEvent) errorMessage() string {
	message := e.Type
	if e.Error != nil && strings.TrimSpace(e.Error.Message) != "" {
		message = strings.TrimSpace(e.Error.Message)
	}
	if runes := []rune(message); len(runes) > maxClientErrorChars {
		message = string(runes[:maxClientErrorChars])
	}
	return message
}

// runTranscription configures input transcription, sends the audio as one manual turn and returns
// the completed transcript, reporting deltas as they arrive.
func runTranscription(conn *websocket.Conn, request transcriptionRequest, pcm []byte, onDelta func(string)) (string, error) {
	transcription := map[string]any{"model": request.model}
	if request.prompt != "" {
		transcription["prompt"] = request.prompt
	}
	if len(request.keywords) > 0 {
		transcription["keywords"] = request.keywords
	}
	if len(request.languages) > 0 {
		transcription["languages"] = request.languages
	}
	update := map[string]any{
		"type": "session.update",
		"session": map[string]any{
			"type":              "realtime",
			"output_modalities": []string{"text"},
			"audio": map[string]any{"input": map[string]any{
				"format":         map[string]any{"type": "audio/pcm", "rate": transcriptionSampleRate},
				"turn_detection": nil,
				"transcription":  transcription,
			}},
		},
	}
	if errWrite := conn.WriteJSON(update); errWrite != nil {
		return "", fmt.Errorf("send session.update: %w", errWrite)
	}
	for {
		event, errRead := readRealtimeEvent(conn)
		if errRead != nil {
			return "", errRead
		}
		if event.Type == "error" {
			return "", upstreamEventError{message: "Realtime session rejected the transcription settings: " + event.errorMessage(), settings: true}
		}
		if event.Type == "session.updated" {
			break
		}
	}

	for offset := 0; offset < len(pcm); offset += transcriptionChunkBytes {
		chunk := pcm[offset:min(offset+transcriptionChunkBytes, len(pcm))]
		if errWrite := conn.WriteJSON(map[string]string{"type": "input_audio_buffer.append", "audio": base64.StdEncoding.EncodeToString(chunk)}); errWrite != nil {
			return "", fmt.Errorf("send audio: %w", errWrite)
		}
	}
	if errWrite := conn.WriteJSON(map[string]string{"type": "input_audio_buffer.commit"}); errWrite != nil {
		return "", fmt.Errorf("commit audio: %w", errWrite)
	}

	for {
		event, errRead := readRealtimeEvent(conn)
		if errRead != nil {
			return "", errRead
		}
		switch event.Type {
		case "conversation.item.input_audio_transcription.delta":
			if event.Delta != "" {
				onDelta(event.Delta)
			}
		case "conversation.item.input_audio_transcription.completed":
			return event.Transcript, nil
		case "conversation.item.input_audio_transcription.failed", "error":
			return "", upstreamEventError{message: "Transcription failed: " + event.errorMessage()}
		}
	}
}

func readRealtimeEvent(conn *websocket.Conn) (realtimeEvent, error) {
	for {
		messageType, payload, errRead := conn.ReadMessage()
		if errRead != nil {
			return realtimeEvent{}, fmt.Errorf("read realtime event: %w", errRead)
		}
		if messageType != websocket.TextMessage {
			continue
		}
		var event realtimeEvent
		if errUnmarshal := json.Unmarshal(payload, &event); errUnmarshal != nil {
			return realtimeEvent{}, fmt.Errorf("decode realtime event: %w", errUnmarshal)
		}
		return event, nil
	}
}

// transcriptionSSE writes the public API's streamed transcription events; the stream starts with
// the first delta so that a failure before it can still answer with an HTTP error status.
type transcriptionSSE struct {
	c       *gin.Context
	enabled bool
	started bool
}

func newTranscriptionSSE(c *gin.Context, enabled bool) *transcriptionSSE {
	return &transcriptionSSE{c: c, enabled: enabled}
}

func (s *transcriptionSSE) write(event any) {
	if !s.started {
		s.started = true
		s.c.Header("Content-Type", "text/event-stream")
		s.c.Header("Cache-Control", "no-cache")
		s.c.Status(http.StatusOK)
	}
	payload, errMarshal := json.Marshal(event)
	if errMarshal != nil {
		return
	}
	if _, errWrite := fmt.Fprintf(s.c.Writer, "data: %s\n\n", payload); errWrite != nil {
		log.WithError(errWrite).Debug("codex transcription: write SSE event failed")
		return
	}
	s.c.Writer.Flush()
}

func (s *transcriptionSSE) delta(text string) {
	if s.enabled {
		s.write(gin.H{"type": "transcript.text.delta", "delta": text})
	}
}

func (s *transcriptionSSE) done(text string) {
	s.write(gin.H{"type": "transcript.text.done", "text": text})
}

func (s *transcriptionSSE) fail(message string) {
	s.write(gin.H{"type": "error", "error": gin.H{"message": message}})
}
