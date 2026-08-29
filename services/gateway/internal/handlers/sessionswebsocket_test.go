package handlers_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	gorilla "github.com/gorilla/websocket"

	"github.com/vamshireddy02/mithyax/gateway/internal/audio"
	"github.com/vamshireddy02/mithyax/gateway/internal/detector"
	"github.com/vamshireddy02/mithyax/gateway/internal/handlers"
	"github.com/vamshireddy02/mithyax/gateway/internal/realtime"
)

func newSessionWSTestServer(t *testing.T, store *realtime.Store) (server *httptest.Server, baseURL string, repo *fakeSessionRepository, analysisRepo *fakeAnalysisRepository) {
	t.Helper()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	repo = newFakeSessionRepository()
	analysisRepo = newFakeAnalysisRepository()
	router.POST("/api/v1/sessions", handlers.NewCreateSession(store, repo))
	router.GET("/api/v1/sessions/ws", handlers.NewSessionWebSocket(store, repo, analysisRepo, logger))

	server = httptest.NewServer(router)
	t.Cleanup(server.Close)

	return server, server.URL, repo, analysisRepo
}

func createSession(t *testing.T, baseURL string) string {
	t.Helper()

	resp, err := http.Post(baseURL+"/api/v1/sessions", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/v1/sessions: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}

	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return body.ID
}

func dialSession(t *testing.T, baseURL, sessionID string) *gorilla.Conn {
	t.Helper()

	wsURL := "ws" + strings.TrimPrefix(baseURL, "http") + "/api/v1/sessions/ws?session_id=" + sessionID
	conn, resp, err := gorilla.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial failed: %v (resp: %v)", err, resp)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func readOutMessage(t *testing.T, conn *gorilla.Conn) realtime.OutMessage {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var msg realtime.OutMessage
	if err := conn.ReadJSON(&msg); err != nil {
		t.Fatalf("ReadJSON: %v", err)
	}
	return msg
}

// TestSessionWebSocket_FullLifecycle drives the whole Phase 4 pipeline
// end to end: create a session, connect, send a frame and an audio
// chunk, and see video_result/audio_result/risk_update come back, then
// end_session and confirm session_ended.
func TestSessionWebSocket_FullLifecycle(t *testing.T) {
	video := &fakeRealtimeVideoAnalyzer{result: &detector.FrameResult{FaceDetected: true, FakeProbability: 0.8, Verdict: "fake"}}
	aud := &fakeRealtimeAudioAnalyzer{result: &audio.Result{FakeScore: 0.9, Verdict: "fake"}}
	store := realtime.NewStore(video, aud, &fakeRealtimeTemporalAnalyzer{}, &fakeRealtimeRiskEngine{}, realtime.DefaultConfig)
	_, baseURL, repo, analysisRepo := newSessionWSTestServer(t, store)

	sessionID := createSession(t, baseURL)
	conn := dialSession(t, baseURL, sessionID)

	started := readOutMessage(t, conn)
	if started.Type != realtime.TypeSessionStarted || started.ID != sessionID || started.Status != "active" {
		t.Fatalf("got %+v, want session_started for %s", started, sessionID)
	}

	frameMsg := realtime.InMessage{Type: realtime.TypeFrame, Data: base64.StdEncoding.EncodeToString([]byte("jpeg-bytes"))}
	if err := conn.WriteJSON(frameMsg); err != nil {
		t.Fatalf("WriteJSON(frame): %v", err)
	}

	videoResult := readOutMessage(t, conn)
	if videoResult.Type != realtime.TypeVideoResult || videoResult.FakeScore == nil || *videoResult.FakeScore != 0.8 {
		t.Fatalf("got %+v, want video_result FakeScore=0.8", videoResult)
	}

	riskAfterFrame := readOutMessage(t, conn)
	if riskAfterFrame.Type != realtime.TypeRiskUpdate {
		t.Fatalf("got %+v, want risk_update after the frame", riskAfterFrame)
	}

	audioMsg := realtime.InMessage{
		Type:     realtime.TypeAudioChunk,
		Data:     base64.StdEncoding.EncodeToString([]byte("audio-bytes")),
		Filename: "chunk.wav",
	}
	if err := conn.WriteJSON(audioMsg); err != nil {
		t.Fatalf("WriteJSON(audio_chunk): %v", err)
	}

	audioResult := readOutMessage(t, conn)
	if audioResult.Type != realtime.TypeAudioResult || audioResult.FakeScore == nil || *audioResult.FakeScore != 0.9 {
		t.Fatalf("got %+v, want audio_result FakeScore=0.9", audioResult)
	}

	riskAfterAudio := readOutMessage(t, conn)
	if riskAfterAudio.Type != realtime.TypeRiskUpdate {
		t.Fatalf("got %+v, want risk_update after the audio chunk", riskAfterAudio)
	}

	if err := conn.WriteJSON(realtime.InMessage{Type: realtime.TypeEndSession}); err != nil {
		t.Fatalf("WriteJSON(end_session): %v", err)
	}

	ended := readOutMessage(t, conn)
	if ended.Type != realtime.TypeSessionEnded || ended.ID != sessionID {
		t.Fatalf("got %+v, want session_ended for %s", ended, sessionID)
	}

	// The session is removed from the store once its connection closes.
	time.Sleep(50 * time.Millisecond) // let the handler's deferred cleanup run
	if _, ok := store.Get(sessionID); ok {
		t.Error("session still present in store after the connection ended")
	}

	// And its final result should have been persisted — the whole point
	// of 7.1.7 is that this record outlives the in-memory session above.
	persisted, err := repo.Get(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("repo.Get(%s) error = %v, want the session to have been persisted", sessionID, err)
	}
	if persisted.Status != "completed" {
		t.Errorf("persisted.Status = %q, want %q", persisted.Status, "completed")
	}
	if persisted.EndedAt == nil {
		t.Error("persisted.EndedAt is nil, want it set")
	}
	if persisted.RiskScore == nil {
		t.Error("persisted.RiskScore is nil, want it set")
	}
	if persisted.Verdict == "" {
		t.Error("persisted.Verdict is empty, want it set")
	}

	// 7.2: the per-modality breakdown behind that risk_score/verdict
	// should have been persisted too — proving *why* the session got the
	// verdict it did, not just what the verdict was.
	analysisResult, err := analysisRepo.GetBySessionID(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("analysisRepo.GetBySessionID(%s) error = %v, want the analysis to have been persisted", sessionID, err)
	}
	if analysisResult.VideoFakeScore == nil || *analysisResult.VideoFakeScore != 0.8 {
		t.Errorf("analysisResult.VideoFakeScore = %v, want 0.8", analysisResult.VideoFakeScore)
	}
	if analysisResult.VideoVerdict != "fake" {
		t.Errorf("analysisResult.VideoVerdict = %q, want %q", analysisResult.VideoVerdict, "fake")
	}
	if analysisResult.AudioFakeScore == nil || *analysisResult.AudioFakeScore != 0.9 {
		t.Errorf("analysisResult.AudioFakeScore = %v, want 0.9", analysisResult.AudioFakeScore)
	}
	if analysisResult.AudioVerdict != "fake" {
		t.Errorf("analysisResult.AudioVerdict = %q, want %q", analysisResult.AudioVerdict, "fake")
	}
	if analysisResult.RiskScore != *persisted.RiskScore {
		t.Errorf("analysisResult.RiskScore = %v, want it to match the session record's %v", analysisResult.RiskScore, *persisted.RiskScore)
	}
	if analysisResult.RiskVerdict != persisted.Verdict {
		t.Errorf("analysisResult.RiskVerdict = %q, want it to match the session record's %q", analysisResult.RiskVerdict, persisted.Verdict)
	}
}

func TestSessionWebSocket_UnknownSessionID_NotFound(t *testing.T) {
	store := realtime.NewStore(&fakeRealtimeVideoAnalyzer{}, &fakeRealtimeAudioAnalyzer{}, &fakeRealtimeTemporalAnalyzer{}, &fakeRealtimeRiskEngine{}, realtime.DefaultConfig)
	_, baseURL, _, _ := newSessionWSTestServer(t, store)

	wsURL := "ws" + strings.TrimPrefix(baseURL, "http") + "/api/v1/sessions/ws?session_id=does-not-exist"
	_, resp, err := gorilla.DefaultDialer.Dial(wsURL, nil)
	if err == nil {
		t.Fatal("dial succeeded, want it to fail for an unknown session_id")
	}
	if resp == nil || resp.StatusCode != http.StatusNotFound {
		status := "<nil response>"
		if resp != nil {
			status = resp.Status
		}
		t.Errorf("response status = %v, want %d", status, http.StatusNotFound)
	}
}

func TestSessionWebSocket_MissingSessionID_BadRequest(t *testing.T) {
	store := realtime.NewStore(&fakeRealtimeVideoAnalyzer{}, &fakeRealtimeAudioAnalyzer{}, &fakeRealtimeTemporalAnalyzer{}, &fakeRealtimeRiskEngine{}, realtime.DefaultConfig)
	_, baseURL, _, _ := newSessionWSTestServer(t, store)

	resp, err := http.Get(baseURL + "/api/v1/sessions/ws")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

// TestSessionWebSocket_DetectorError_SendsErrorMessageAndStaysOpen
// proves one failed frame doesn't kill the session — only that message
// gets an error reply, and the connection stays open for the next one.
func TestSessionWebSocket_DetectorError_SendsErrorMessageAndStaysOpen(t *testing.T) {
	video := &erroringVideoAnalyzer{}
	store := realtime.NewStore(video, &fakeRealtimeAudioAnalyzer{}, &fakeRealtimeTemporalAnalyzer{}, &fakeRealtimeRiskEngine{}, realtime.DefaultConfig)
	_, baseURL, _, _ := newSessionWSTestServer(t, store)

	sessionID := createSession(t, baseURL)
	conn := dialSession(t, baseURL, sessionID)
	readOutMessage(t, conn) // session_started

	frameMsg := realtime.InMessage{Type: realtime.TypeFrame, Data: base64.StdEncoding.EncodeToString([]byte("jpeg-bytes"))}
	if err := conn.WriteJSON(frameMsg); err != nil {
		t.Fatalf("WriteJSON(frame): %v", err)
	}

	got := readOutMessage(t, conn)
	if got.Type != realtime.TypeError || got.Message == "" {
		t.Fatalf("got %+v, want a non-empty error message", got)
	}

	// The connection itself should still be usable — send a second,
	// this time undecodable, message and confirm we still get a reply
	// rather than the connection having silently died.
	if err := conn.WriteJSON(realtime.InMessage{Type: realtime.TypeFrame, Data: "not-base64!!"}); err != nil {
		t.Fatalf("WriteJSON(second frame): %v", err)
	}
	got = readOutMessage(t, conn)
	if got.Type != realtime.TypeError {
		t.Fatalf("got %+v, want another error (invalid base64), proving the session is still alive", got)
	}
}

type erroringVideoAnalyzer struct{}

func (e *erroringVideoAnalyzer) AnalyzeFrame(ctx context.Context, jpeg []byte) (*detector.FrameResult, error) {
	return nil, &detector.Error{Kind: detector.KindUnavailable, Message: "video-detector unreachable"}
}

// blockingAudioAnalyzer never returns until unblock is closed — used to
// force an audio queue to genuinely fill up under test, rather than
// racing against real inference latency.
type blockingAudioAnalyzer struct {
	unblock chan struct{}
}

func (b *blockingAudioAnalyzer) Analyze(ctx context.Context, filename string, data []byte) (*audio.Result, error) {
	select {
	case <-b.unblock:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &audio.Result{FakeScore: 0.1, Verdict: "real"}, nil
}

// TestSessionWebSocket_OverloadedAudioQueue_EmitsOverloadedErrorCode
// proves the wire-level shape the task calls for — {"type":"error",
// "code":"overloaded"} — actually reaches the browser when a session's
// audio queue genuinely can't accept more work.
func TestSessionWebSocket_OverloadedAudioQueue_EmitsOverloadedErrorCode(t *testing.T) {
	aud := &blockingAudioAnalyzer{unblock: make(chan struct{})}
	defer close(aud.unblock)

	cfg := realtime.Config{MaxVideoQueue: 5, VideoWorkers: 1, MaxAudioQueue: 1, AudioWorkers: 1, MaxSessions: 10}
	store := realtime.NewStore(&fakeRealtimeVideoAnalyzer{}, aud, &fakeRealtimeTemporalAnalyzer{}, &fakeRealtimeRiskEngine{}, cfg)
	_, baseURL, _, _ := newSessionWSTestServer(t, store)

	sessionID := createSession(t, baseURL)
	conn := dialSession(t, baseURL, sessionID)
	readOutMessage(t, conn) // session_started

	audioMsg := func() realtime.InMessage {
		return realtime.InMessage{
			Type:     realtime.TypeAudioChunk,
			Data:     base64.StdEncoding.EncodeToString([]byte("audio-bytes")),
			Filename: "chunk.wav",
		}
	}

	// First chunk: picked up by the sole worker and blocks there.
	if err := conn.WriteJSON(audioMsg()); err != nil {
		t.Fatalf("WriteJSON(chunk 1): %v", err)
	}
	// Second chunk: fills the queue's one slot.
	if err := conn.WriteJSON(audioMsg()); err != nil {
		t.Fatalf("WriteJSON(chunk 2): %v", err)
	}
	// Give the server a moment to actually enqueue both before the third
	// arrives — otherwise this could race and land in the still-empty
	// queue slot instead of genuinely overflowing it.
	time.Sleep(100 * time.Millisecond)
	// Third chunk: queue is full, must be rejected.
	if err := conn.WriteJSON(audioMsg()); err != nil {
		t.Fatalf("WriteJSON(chunk 3): %v", err)
	}

	got := readOutMessage(t, conn)
	if got.Type != realtime.TypeError || got.Code != realtime.ErrCodeOverloaded {
		t.Fatalf("got %+v, want {type: error, code: overloaded}", got)
	}
}
