package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/vamshireddy02/mithyax/gateway/internal/analysisjob"
	"github.com/vamshireddy02/mithyax/gateway/internal/queue"
	jobsrepo "github.com/vamshireddy02/mithyax/gateway/internal/repository/jobs"
	"github.com/vamshireddy02/mithyax/gateway/internal/security"
)

// maxURLLength bounds video_url/audio_url (7.7.6) — 2048 is the
// conventional ceiling most browsers and servers already treat as a
// practical URL length limit, comfortably larger than any real media
// URL. Checked before SSRF validation: it's a cheaper, purely
// syntactic rejection that also happens to be exactly what keeps an
// AnalysisJob's payload (just a URL string — see internal/analysisjob)
// small and predictable before it's ever persisted to Postgres or
// pushed onto the Redis queue.
const maxURLLength = 2048

// createAnalysisRequest is the POST /api/v1/analysis body (7.6.1):
// session_id plus at least one of video_url/audio_url. Requesting both
// creates two independent jobs sharing session_id (7.6.2).
type createAnalysisRequest struct {
	SessionID string `json:"session_id"`
	VideoURL  string `json:"video_url"`
	AudioURL  string `json:"audio_url"`
}

// analysisJobSummary is one created job's entry in the response.
type analysisJobSummary struct {
	JobID  string `json:"job_id"`
	Type   string `json:"type"`
	Status string `json:"status"`
}

// createAnalysisResponse always carries the full Jobs list; when
// exactly one job was created, JobID/Status are also set at the top
// level so a single-modality request gets back the exact
// {"job_id": ..., "status": "queued"} shape the ticket specifies,
// without a combined request needing a second, incompatible shape.
type createAnalysisResponse struct {
	JobID     string               `json:"job_id,omitempty"`
	Status    string               `json:"status,omitempty"`
	SessionID string               `json:"session_id"`
	Jobs      []analysisJobSummary `json:"jobs"`
}

// NewCreateAnalysisJob builds the POST /api/v1/analysis handler
// (7.6.1/7.6.2): validate the request, create an AnalysisJob per
// requested modality, persist each as StatusQueued in Postgres, then
// enqueue into the matching Redis queue, and return 202 immediately —
// no inference happens here.
//
// The durable jobs record is written before the Redis enqueue,
// deliberately: a client polling GET /api/v1/analysis/jobs/:id must
// never get a 404 for a job_id this handler already returned. If the
// enqueue then fails, the job is marked dead_letter rather than left
// stuck at "queued" forever with nothing ever going to pick it up.
func NewCreateAnalysisJob(videoQueue, audioQueue queue.Queue, jobs jobsrepo.Repository, urlValidator *security.Validator, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req createAnalysisRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
			return
		}
		if req.SessionID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "session_id is required"})
			return
		}
		if req.VideoURL == "" && req.AudioURL == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "at least one of video_url or audio_url is required"})
			return
		}
		if len(req.VideoURL) > maxURLLength {
			c.JSON(http.StatusBadRequest, gin.H{"error": "video_url exceeds maximum length"})
			return
		}
		if len(req.AudioURL) > maxURLLength {
			c.JSON(http.StatusBadRequest, gin.H{"error": "audio_url exceeds maximum length"})
			return
		}

		// SSRF protection, layer 1 of 2 (7.7.5): reject an obviously
		// unsafe URL before it's ever persisted or enqueued. This alone
		// isn't sufficient — a job can sit in Redis for a long time
		// before a worker picks it up, and DNS can change in the
		// meantime — so the worker validates again immediately before
		// fetching (see internal/analysisworker's Handler
		// implementations). Both checks share this same Validator.
		if req.VideoURL != "" {
			if err := urlValidator.ValidateURL(req.VideoURL); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "video_url is not allowed: " + err.Error()})
				return
			}
		}
		if req.AudioURL != "" {
			if err := urlValidator.ValidateURL(req.AudioURL); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "audio_url is not allowed: " + err.Error()})
				return
			}
		}

		var summaries []analysisJobSummary

		if req.VideoURL != "" {
			summary, err := createAndEnqueue(c, videoQueue, jobs, logger, func() (analysisjob.AnalysisJob, error) {
				return analysisjob.NewVideoAnalysisJob(req.SessionID, req.VideoURL)
			})
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create video analysis job"})
				return
			}
			summaries = append(summaries, summary)
		}

		if req.AudioURL != "" {
			summary, err := createAndEnqueue(c, audioQueue, jobs, logger, func() (analysisjob.AnalysisJob, error) {
				return analysisjob.NewAudioAnalysisJob(req.SessionID, req.AudioURL)
			})
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create audio analysis job"})
				return
			}
			summaries = append(summaries, summary)
		}

		resp := createAnalysisResponse{SessionID: req.SessionID, Jobs: summaries}
		if len(summaries) == 1 {
			resp.JobID = summaries[0].JobID
			resp.Status = summaries[0].Status
		}
		c.JSON(http.StatusAccepted, resp)
	}
}

// createAndEnqueue builds one AnalysisJob via newJob, persists it as
// StatusQueued, and enqueues it into q. Shared by both modalities in
// NewCreateAnalysisJob since the sequencing (and its failure handling)
// is identical either way.
func createAndEnqueue(c *gin.Context, q queue.Queue, jobs jobsrepo.Repository, logger *slog.Logger, newJob func() (analysisjob.AnalysisJob, error)) (analysisJobSummary, error) {
	ctx := c.Request.Context()

	job, err := newJob()
	if err != nil {
		return analysisJobSummary{}, err
	}

	record := jobsrepo.Job{
		ID:          job.ID,
		SessionID:   job.SessionID,
		Type:        string(job.Type),
		Status:      jobsrepo.StatusQueued,
		Attempt:     job.Attempt,
		MaxAttempts: job.MaxAttempts,
		CreatedAt:   job.CreatedAt,
	}
	if err := jobs.Create(ctx, record); err != nil {
		return analysisJobSummary{}, err
	}

	qj, err := job.ToQueueJob()
	if err != nil {
		markDeadLetterBestEffort(ctx, jobs, logger, job.ID, "failed to prepare job for queue: "+err.Error())
		return analysisJobSummary{}, err
	}
	if err := q.Enqueue(ctx, qj); err != nil {
		markDeadLetterBestEffort(ctx, jobs, logger, job.ID, "failed to enqueue: "+err.Error())
		return analysisJobSummary{}, err
	}

	return analysisJobSummary{JobID: job.ID, Type: string(job.Type), Status: string(jobsrepo.StatusQueued)}, nil
}

// markDeadLetterBestEffort records that a job which was already
// persisted as StatusQueued never actually made it onto the queue, so
// a client polling it doesn't see "queued" forever. Errors here are
// logged, not returned — the caller already has its own error to
// report to the HTTP client.
func markDeadLetterBestEffort(ctx context.Context, jobs jobsrepo.Repository, logger *slog.Logger, jobID, reason string) {
	if err := jobs.MarkDeadLetter(ctx, jobID, reason); err != nil {
		logger.Error("failed to mark job dead-letter after enqueue failure",
			slog.String("job_id", jobID), slog.String("error", err.Error()))
	}
}

// NewGetAnalysisJob builds the GET /api/v1/analysis/jobs/:id handler
// (7.6.4): a client's way to poll an async job's status without
// needing Redis reachable — see internal/repository/jobs's package
// doc for why Postgres, not the queue, is the source of truth here.
func NewGetAnalysisJob(jobs jobsrepo.Repository) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")

		job, err := jobs.Get(c.Request.Context(), id)
		if errors.Is(err, jobsrepo.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "job not found"})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch job"})
			return
		}

		c.JSON(http.StatusOK, job)
	}
}
