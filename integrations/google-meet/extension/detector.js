// detector.js — folds the session websocket's messages into one running
// risk state (REAL / SUSPICIOUS / AI-HIGH-RISK). Lives in background.js:
// it's the single source of truth the badge renders from, so the
// extension never scores anything itself — it only relays and labels
// what the Risk Engine already decided.

export const VERDICT_META = {
  LIKELY_AUTHENTIC: { tier: "real", label: "Real" },
  SUSPICIOUS: { tier: "suspicious", label: "Suspicious" },
  LIKELY_FAKE: { tier: "fake", label: "AI / High risk" },
  UNKNOWN: { tier: "unknown", label: "Analyzing" },
};

export function createDetectorState() {
  return {
    tier: "unknown",
    label: "Analyzing",
    riskScore: null,
    videoScore: null,
    audioScore: null,
    temporalScore: null,
    reasons: [],
  };
}

export function applyMessage(state, message) {
  switch (message.type) {
    case "video_result":
      return { ...state, videoScore: message.face_detected ? message.fake_score : null };

    case "audio_result":
      return { ...state, audioScore: message.fake_score };

    case "temporal_result":
      return { ...state, temporalScore: message.score };

    case "risk_update": {
      const meta = VERDICT_META[message.verdict] || VERDICT_META.UNKNOWN;
      return {
        ...state,
        tier: meta.tier,
        label: meta.label,
        riskScore: message.risk_score,
        reasons: message.reasons || [],
      };
    }

    default:
      return state;
  }
}
