package live

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type fakeTranscriptionUpstream struct {
	server        *httptest.Server
	authorization chan string
	update        chan map[string]any
	audio         chan []byte
}

// newFakeTranscriptionUpstream answers session.update, collects appended audio until commit, then
// replays reply as the transcription events.
func newFakeTranscriptionUpstream(t *testing.T, reply ...string) *fakeTranscriptionUpstream {
	t.Helper()
	upstream := &fakeTranscriptionUpstream{
		authorization: make(chan string, 1),
		update:        make(chan map[string]any, 1),
		audio:         make(chan []byte, 1),
	}
	upstream.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, errUpgrade := upgrader.Upgrade(writer, request, nil)
		if errUpgrade != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		upstream.authorization <- request.Header.Get("Authorization")
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"session.created"}`))
		var audio []byte
		for {
			var event map[string]any
			if errRead := conn.ReadJSON(&event); errRead != nil {
				return
			}
			switch event["type"] {
			case "session.update":
				upstream.update <- event
				_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"session.updated"}`))
			case "input_audio_buffer.append":
				chunk, _ := base64.StdEncoding.DecodeString(event["audio"].(string))
				audio = append(audio, chunk...)
			case "input_audio_buffer.commit":
				upstream.audio <- audio
				for _, message := range reply {
					_ = conn.WriteMessage(websocket.TextMessage, []byte(message))
				}
			}
		}
	}))
	t.Cleanup(upstream.server.Close)
	return upstream
}

func newTranscriptionRouter(t *testing.T, upstreamURL string, pcm []byte) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	previous := decodeTranscriptionAudio
	decodeTranscriptionAudio = func(context.Context, []byte) ([]byte, error) { return pcm, nil }
	t.Cleanup(func() { decodeTranscriptionAudio = previous })

	manager := auth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(&captureExecutor{})
	registerCredential(t, manager, &auth.Auth{
		ID:       "codex-oauth",
		Provider: "codex",
		Status:   auth.StatusActive,
		Metadata: map[string]any{"access_token": "oauth-token", "account_id": "account-123"},
	})
	handler := NewHandler(manager, nil)
	handler.sidebandAPIBaseURL = "ws" + strings.TrimPrefix(upstreamURL, "http") + "/v1"
	router := gin.New()
	router.POST("/v1/audio/transcriptions", handler.HandleAudioTranscription)
	return router
}

func transcriptionForm(t *testing.T, fields map[string][]string) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	file, errFile := form.CreateFormFile("file", "audio.webm")
	if errFile != nil {
		t.Fatal(errFile)
	}
	_, _ = file.Write([]byte("encoded audio"))
	for name, values := range fields {
		for _, value := range values {
			_ = form.WriteField(name, value)
		}
	}
	_ = form.Close()
	return &body, form.FormDataContentType()
}

func postTranscription(router *gin.Engine, body *bytes.Buffer, contentType string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", body)
	request.Header.Set("Content-Type", contentType)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func TestHandleAudioTranscriptionStreamsRealtimeTranscript(t *testing.T) {
	pcm := bytes.Repeat([]byte{1, 2}, transcriptionChunkBytes) // two chunks of speech
	upstream := newFakeTranscriptionUpstream(t,
		`{"type":"conversation.item.input_audio_transcription.delta","delta":"git "}`,
		`{"type":"conversation.item.input_audio_transcription.delta","delta":"status"}`,
		`{"type":"conversation.item.input_audio_transcription.completed","transcript":"git status"}`,
	)
	router := newTranscriptionRouter(t, upstream.server.URL, pcm)
	body, contentType := transcriptionForm(t, map[string][]string{
		"model":       {"gpt-transcribe"},
		"stream":      {"true"},
		"prompt":      {"A developer dictating."},
		"keywords[]":  {"git", "status"},
		"languages[]": {"ko", "en"},
	})

	recorder := postTranscription(router, body, contentType)

	if recorder.Code != http.StatusOK || !strings.HasPrefix(recorder.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status = %d, content-type = %q, body = %s", recorder.Code, recorder.Header().Get("Content-Type"), recorder.Body.String())
	}
	want := "data: {\"delta\":\"git \",\"type\":\"transcript.text.delta\"}\n\n" +
		"data: {\"delta\":\"status\",\"type\":\"transcript.text.delta\"}\n\n" +
		"data: {\"text\":\"git status\",\"type\":\"transcript.text.done\"}\n\n"
	if recorder.Body.String() != want {
		t.Fatalf("SSE body = %q, want %q", recorder.Body.String(), want)
	}
	if got := <-upstream.authorization; got != "Bearer oauth-token" {
		t.Fatalf("Authorization = %q", got)
	}
	update := <-upstream.update
	input := update["session"].(map[string]any)["audio"].(map[string]any)["input"].(map[string]any)
	if input["turn_detection"] != nil {
		t.Fatalf("turn_detection = %v, want null so the commit ends the turn", input["turn_detection"])
	}
	transcription, _ := json.Marshal(input["transcription"])
	if string(transcription) != `{"keywords":["git","status"],"languages":["ko","en"],"model":"gpt-transcribe","prompt":"A developer dictating."}` {
		t.Fatalf("transcription = %s", transcription)
	}
	if audio := <-upstream.audio; !bytes.Equal(audio, pcm) {
		t.Fatalf("appended %d bytes, want the %d decoded bytes unchanged", len(audio), len(pcm))
	}
}

func TestHandleAudioTranscriptionAnswersJSONAndDefaultsModel(t *testing.T) {
	upstream := newFakeTranscriptionUpstream(t,
		`{"type":"conversation.item.input_audio_transcription.delta","delta":"hello"}`,
		`{"type":"conversation.item.input_audio_transcription.completed","transcript":"hello"}`,
	)
	router := newTranscriptionRouter(t, upstream.server.URL, make([]byte, transcriptionMinSpeechBytes))
	body, contentType := transcriptionForm(t, map[string][]string{"language": {"ko"}})

	recorder := postTranscription(router, body, contentType)

	if recorder.Code != http.StatusOK || recorder.Body.String() != `{"text":"hello"}` {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	update := <-upstream.update
	transcription, _ := json.Marshal(update["session"].(map[string]any)["audio"].(map[string]any)["input"].(map[string]any)["transcription"])
	if string(transcription) != `{"languages":["ko"],"model":"gpt-live-transcribe"}` {
		t.Fatalf("transcription = %s", transcription)
	}
}

func TestHandleAudioTranscriptionReportsUpstreamFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stream string
		reply  []string
		status int
		body   string
	}{
		{
			name:   "failure before any text answers an HTTP error",
			reply:  []string{`{"type":"conversation.item.input_audio_transcription.failed","error":{"message":"audio unreadable"}}`},
			status: http.StatusBadGateway,
			body:   "Transcription failed: audio unreadable",
		},
		{
			name:   "streamed failure before any text still answers an HTTP error",
			stream: "true",
			reply:  []string{`{"type":"error","error":{"message":"server overloaded"}}`},
			status: http.StatusBadGateway,
			body:   `"message":"Transcription failed: server overloaded"`,
		},
		{
			name:   "failure after streamed text ends the stream with an error event",
			stream: "true",
			reply: []string{
				`{"type":"conversation.item.input_audio_transcription.delta","delta":"git"}`,
				`{"type":"error","error":{"message":"server overloaded"}}`,
			},
			status: http.StatusOK,
			body:   `data: {"error":{"message":"Transcription failed: server overloaded"},"type":"error"}`,
		},
		{
			name:   "upstream that hangs up answers a fixed message",
			reply:  []string{"not json"},
			status: http.StatusBadGateway,
			body:   "Realtime transcription session ended unexpectedly",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := newFakeTranscriptionUpstream(t, tc.reply...)
			router := newTranscriptionRouter(t, upstream.server.URL, make([]byte, transcriptionMinSpeechBytes))
			body, contentType := transcriptionForm(t, map[string][]string{"stream": {tc.stream}})

			recorder := postTranscription(router, body, contentType)

			if recorder.Code != tc.status || !strings.Contains(recorder.Body.String(), tc.body) {
				t.Fatalf("status = %d, body = %s; want %d containing %q", recorder.Code, recorder.Body.String(), tc.status, tc.body)
			}
		})
	}
}

func TestHandleAudioTranscriptionRejectsTooShortAudioWithoutUpstream(t *testing.T) {
	upstream := newFakeTranscriptionUpstream(t)
	router := newTranscriptionRouter(t, upstream.server.URL, make([]byte, transcriptionMinSpeechBytes-2))
	body, contentType := transcriptionForm(t, nil)

	recorder := postTranscription(router, body, contentType)

	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "audio_too_short") {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	select {
	case <-upstream.authorization:
		t.Fatal("a too-short recording must not open a Realtime session")
	default:
	}
}

func TestHandleAudioTranscriptionAnswersRejectedSettingsAsClientError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, errUpgrade := upgrader.Upgrade(writer, request, nil)
		if errUpgrade != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		var update map[string]any
		if errRead := conn.ReadJSON(&update); errRead != nil {
			return
		}
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"error","error":{"message":"The model `+"`no-such-model`"+` does not exist"}}`))
		_, _, _ = conn.ReadMessage()
	}))
	defer upstream.Close()
	router := newTranscriptionRouter(t, upstream.URL, make([]byte, transcriptionMinSpeechBytes))
	body, contentType := transcriptionForm(t, map[string][]string{"model": {"no-such-model"}, "stream": {"true"}})

	recorder := postTranscription(router, body, contentType)

	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "invalid_transcription_settings") || !strings.Contains(recorder.Body.String(), "no-such-model") {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestHandleAudioTranscriptionPassesUpstreamRateLimit(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Retry-After", "17")
		writer.WriteHeader(http.StatusTooManyRequests)
	}))
	defer upstream.Close()
	router := newTranscriptionRouter(t, upstream.URL, make([]byte, transcriptionMinSpeechBytes))
	body, contentType := transcriptionForm(t, nil)

	recorder := postTranscription(router, body, contentType)

	if recorder.Code != http.StatusTooManyRequests || recorder.Header().Get("Retry-After") != "17" {
		t.Fatalf("status = %d, Retry-After = %q, body = %s", recorder.Code, recorder.Header().Get("Retry-After"), recorder.Body.String())
	}
}

func TestHandleAudioTranscriptionRejectsOversizedUpload(t *testing.T) {
	upstream := newFakeTranscriptionUpstream(t)
	router := newTranscriptionRouter(t, upstream.server.URL, make([]byte, transcriptionMinSpeechBytes))
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	file, _ := form.CreateFormFile("file", "audio.webm")
	_, _ = file.Write(make([]byte, maxTranscriptionAudioBytes+transcriptionFormSlack))
	_ = form.Close()

	recorder := postTranscription(router, &body, form.FormDataContentType())

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestDecodeWithFFmpegDecodesRecordingsOnly(t *testing.T) {
	ffmpeg, errLook := exec.LookPath("ffmpeg")
	if errLook != nil {
		t.Skip("ffmpeg not installed")
	}
	// uploads are written to TMPDIR: put them beside the file a playlist would try to read
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	wav := filepath.Join(dir, "two-seconds.wav")
	if output, errRun := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=2", wav).CombinedOutput(); errRun != nil {
		t.Fatalf("make wav: %v: %s", errRun, output)
	}
	recording, errRead := os.ReadFile(wav)
	if errRead != nil {
		t.Fatal(errRead)
	}

	pcm, errDecode := decodeWithFFmpeg(context.Background(), recording)
	if errDecode != nil || len(pcm) != 2*transcriptionSampleRate*2 {
		t.Fatalf("decoded %d bytes, err %v; want 2 s of 24 kHz mono PCM16", len(pcm), errDecode)
	}

	previous := maxTranscriptionSeconds
	maxTranscriptionSeconds = 1
	_, errLong := decodeWithFFmpeg(context.Background(), recording)
	maxTranscriptionSeconds = previous
	if !errors.Is(errLong, errAudioTooLong) {
		t.Fatalf("2 s recording with a 1 s cap: err = %v, want errAudioTooLong", errLong)
	}

	// a concat playlist naming another file in the same temp dir must not be followed
	playlist := []byte("ffconcat version 1.0\nfile " + filepath.Base(wav) + "\n")
	if _, errPlaylist := decodeWithFFmpeg(context.Background(), playlist); errPlaylist == nil {
		t.Fatal("a playlist upload was decoded; only recordings may be")
	}
}
