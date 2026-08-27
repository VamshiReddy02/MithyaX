package realtime

// Config bounds a live session's resource usage: how deep its video and
// audio queues are allowed to grow, how many workers drain each, and
// how many concurrent sessions a Store will run. These come from
// environment variables (see internal/config.Config's Realtime* fields)
// rather than being hardcoded — the right values depend on the detector
// services' actual throughput and the hardware running them.
type Config struct {
	// MaxVideoQueue bounds how many frames wait for a video worker.
	// Once full, the oldest queued frame is dropped for the newest —
	// see Session.SubmitFrame.
	MaxVideoQueue int
	// VideoWorkers is how many goroutines pull frames off the video
	// queue and call the video-detector concurrently.
	VideoWorkers int
	// MaxAudioQueue bounds how many audio chunks wait for an audio
	// worker. Once full, the new chunk is rejected rather than an old
	// one dropped — see Session.SubmitAudioChunk.
	MaxAudioQueue int
	// AudioWorkers is how many goroutines pull chunks off the audio
	// queue and call the audio-detector concurrently.
	AudioWorkers int
	// MaxSessions bounds how many live Sessions a Store will run at
	// once.
	MaxSessions int
}

// DefaultConfig is used by anything that doesn't wire its own Config —
// tests, mainly; production wiring always passes an explicit Config
// built from environment variables (see httpserver.New).
var DefaultConfig = Config{
	MaxVideoQueue: 10,
	VideoWorkers:  2,
	MaxAudioQueue: 10,
	AudioWorkers:  2,
	MaxSessions:   100,
}
