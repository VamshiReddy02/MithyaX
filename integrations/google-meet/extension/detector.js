// detector.js — folds the session websocket's messages into one running
// display state. Lives in background.js: it's the single source of
// truth the badge renders from, so the extension never scores anything
// itself — it only relays and translates what the Risk Engine already
// decided into something a first-time user can understand at a glance
// (Phase 8.8).
//
// The state this produces is a discriminated union on `kind`:
//   "analyzing" — session is live, no confident verdict yet (this is
//                 also what a risk_update with verdict "UNKNOWN" maps
//                 to — the risk engine had no usable signal at all,
//                 which reads to a user exactly the same as "still
//                 gathering data", not as a fourth verdict of its own).
//   "verdict"   — a real, confident verdict: tier + human headline/
//                 description + a derived confidence percentage +
//                 reasons (already human-readable sentences from the
//                 gateway's own risk engine, e.g. "Video signal
//                 indicates likely synthetic or manipulated content" —
//                 never a raw score). videoScore/audioScore/
//                 temporalScore are kept on the state for developer
//                 console debugging only; ui.js must never render them.
// content.js constructs the other kinds itself — "connecting",
// "reconnecting", "unavailable", "left" — since those are operational
// facts about the connection, not anything the risk engine reported;
// see content.js's own port message handling for those.
export const VERDICT_META = {
  LIKELY_AUTHENTIC: {
    tier: "real",
    headline: "Likely Authentic",
    description: "No signs of AI manipulation detected.",
  },
  SUSPICIOUS: {
    tier: "suspicious",
    headline: "Suspicious",
    description: "MithyaX detected signals that may indicate AI manipulation.",
  },
  LIKELY_FAKE: {
    tier: "fake",
    headline: "Likely AI-Generated",
    description: "MithyaX detected strong signals of AI manipulation.",
  },
};

const ANALYZING_HEADLINE = "Analyzing";
const ANALYZING_DESCRIPTION = "Gathering enough signal to make an assessment.";

export function createDetectorState() {
  return {
    kind: "analyzing",
    tier: "unknown",
    headline: ANALYZING_HEADLINE,
    description: ANALYZING_DESCRIPTION,
    confidence: null,
    reasons: [],
    videoScore: null,
    audioScore: null,
    temporalScore: null,
  };
}

// confidencePercent turns a [0,1] fake-likelihood score into "how
// confident should a user be in the verdict they're looking at" — not
// the same number. A low fake-score is why something is called
// authentic, so confidence-in-that-verdict rises as the score falls
// toward 0; a high score is why something is called fake, so
// confidence rises as the score climbs toward 1. Suspicious sits in the
// engine's own deliberately uncertain middle band (see internal/risk/
// verdict.go's Thresholds), where the raw score already reads
// naturally as "how strongly signals lean toward fake" — a rescaling
// there would just be an extra transformation with no clearer meaning.
function confidencePercent(tier, score) {
  if (score == null) return null;
  const fraction = tier === "real" ? 1 - score : score;
  return Math.round(fraction * 100);
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
      const meta = VERDICT_META[message.verdict];
      if (!meta) {
        // "UNKNOWN" (no usable signal yet) or any verdict this build
        // doesn't recognize — both read the same to a user as "still
        // working on it", not a fourth kind of result.
        return { ...state, kind: "analyzing", tier: "unknown", headline: ANALYZING_HEADLINE, description: ANALYZING_DESCRIPTION, confidence: null };
      }
      return {
        ...state,
        kind: "verdict",
        tier: meta.tier,
        headline: meta.headline,
        description: meta.description,
        confidence: confidencePercent(meta.tier, message.risk_score),
        reasons: message.reasons || [],
      };
    }

    default:
      return state;
  }
}
