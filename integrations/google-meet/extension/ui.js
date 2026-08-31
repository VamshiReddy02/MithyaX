// ui.js — the floating MithyaX badges overlaid on the Meet page, one per
// tracked remote participant (Phase 8.4). Purely presentational: it
// renders whatever state background.js/detector.js/content.js send down
// for a given participant key, it never computes a verdict itself.
//
// Phase 8.8: updateBadge's `state` is a discriminated union on `kind` —
// see detector.js's own doc for "analyzing"/"verdict" (risk-engine-
// driven) and content.js's port message handling for "connecting"/
// "reconnecting"/"unavailable"/"left" (connection-driven, not a risk
// signal). The one rule this file exists to enforce: nothing here ever
// renders a raw model score (state.videoScore/audioScore/temporalScore
// are on the state object for developer console debugging only) — only
// the human headline/description/confidence detector.js already derived,
// and the reasons the gateway's own risk engine already phrases as
// sentences, never a bare number.
//
// Built with DOM APIs rather than innerHTML — meet.google.com enforces
// a Trusted Types CSP that throws on raw innerHTML assignment from
// content scripts.
(function () {
  const TIER_EMOJI = {
    real: "🟢",
    suspicious: "🟡",
    fake: "🔴",
    unknown: "⚪",
  };

  // Emoji/label for every non-"verdict" kind — connection/lifecycle
  // states, not a risk result. "unavailable" gets its own emoji (⚠️,
  // not 🔴) and its own data-tier value deliberately: a connection
  // problem must never look like — or be stylable the same as — an
  // actual "likely fake" verdict.
  const KIND_EMOJI = {
    connecting: "⚪",
    analyzing: "⚪",
    reconnecting: "🔄",
    unavailable: "⚠️",
    left: "👋",
  };
  const KIND_LABEL = {
    connecting: "Connecting…",
    analyzing: "Analyzing…",
    unavailable: "Analysis unavailable",
    left: "Participant left",
  };

  // One entry per participant key. Was a handful of module-level
  // singletons before 8.4 — every function below now takes a key to
  // know which participant's badge it's touching, so one participant's
  // badge never affects another's.
  const badges = new Map();

  function ensureBadge(key) {
    const existing = badges.get(key);
    if (existing && document.body.contains(existing.badgeEl)) return existing.badgeEl;

    // A class, not an id — ids must be unique per document, and 8.4
    // needs one badge per participant on screen at once.
    const badgeEl = document.createElement("div");
    badgeEl.className = "mithyax-badge";

    const pillEl = document.createElement("div");
    pillEl.className = "mithyax-pill";

    const dotEl = document.createElement("span");
    dotEl.className = "mithyax-dot";
    dotEl.textContent = TIER_EMOJI.unknown;

    const label = document.createElement("span");
    label.className = "mithyax-label";
    label.textContent = "MithyaX";

    const verdictEl = document.createElement("span");
    verdictEl.className = "mithyax-verdict";
    verdictEl.textContent = "Connecting…";

    // Only shown for a real verdict — nothing to explain about
    // "Connecting…"/"Reconnecting…" yet. pointer-events is re-enabled
    // just on this element (and the details panel it opens), overriding
    // the badge's own pointer-events:none — that's deliberately off
    // everywhere else on the badge so it never blocks clicks through to
    // Meet's own controls underneath it.
    const toggleEl = document.createElement("button");
    toggleEl.type = "button";
    toggleEl.className = "mithyax-why-toggle";
    toggleEl.textContent = "Why?";
    toggleEl.hidden = true;

    pillEl.append(dotEl, label, verdictEl, toggleEl);

    const detailsEl = document.createElement("div");
    detailsEl.className = "mithyax-details";
    detailsEl.hidden = true;

    const descriptionEl = document.createElement("p");
    descriptionEl.className = "mithyax-details-description";

    const confidenceEl = document.createElement("p");
    confidenceEl.className = "mithyax-details-confidence";

    const reasonsEl = document.createElement("ul");
    reasonsEl.className = "mithyax-details-reasons";

    detailsEl.append(descriptionEl, confidenceEl, reasonsEl);
    badgeEl.append(pillEl, detailsEl);

    toggleEl.addEventListener("click", () => {
      detailsEl.hidden = !detailsEl.hidden;
      toggleEl.textContent = detailsEl.hidden ? "Why?" : "Hide";
    });

    document.body.appendChild(badgeEl);

    const entry = { badgeEl, dotEl, verdictEl, toggleEl, detailsEl, descriptionEl, confidenceEl, reasonsEl };
    badges.set(key, entry);
    return badgeEl;
  }

  function updateBadge(key, state) {
    ensureBadge(key);
    const b = badges.get(key);
    const kind = state.kind || "analyzing";

    if (kind === "verdict") {
      b.dotEl.textContent = TIER_EMOJI[state.tier] || TIER_EMOJI.unknown;
      b.verdictEl.textContent = state.headline || KIND_LABEL.analyzing;
      b.badgeEl.dataset.tier = state.tier || "unknown";

      b.toggleEl.hidden = false;
      b.descriptionEl.textContent = state.description || "";
      b.confidenceEl.textContent = state.confidence == null ? "" : `Confidence: ${state.confidence}%`;
      b.reasonsEl.replaceChildren(
        ...(state.reasons || []).map((reason) => {
          const li = document.createElement("li");
          li.textContent = reason;
          return li;
        }),
      );

      // A hover tooltip mirrors the same details as a zero-click
      // fallback to the click-to-expand panel above.
      b.badgeEl.title = state.description ? `${state.description}${state.confidence == null ? "" : ` (Confidence: ${state.confidence}%)`}` : "";
    } else {
      b.dotEl.textContent = KIND_EMOJI[kind] || KIND_EMOJI.analyzing;
      b.verdictEl.textContent = kind === "reconnecting" ? `Reconnecting… (${state.attempt})` : KIND_LABEL[kind] || KIND_LABEL.analyzing;
      b.badgeEl.dataset.tier = kind === "unavailable" ? "unavailable" : "unknown";

      // Nothing to explain for a connection/lifecycle state — collapse
      // and hide the toggle rather than leaving a stale verdict's
      // details visible underneath an unrelated "Reconnecting…" badge.
      b.toggleEl.hidden = true;
      b.detailsEl.hidden = true;
      b.badgeEl.title = "";
    }
  }

  function removeBadge(key) {
    const existing = badges.get(key);
    if (existing) existing.badgeEl.remove();
    badges.delete(key);
  }

  const TILE_INSET_PX = 12;

  // Anchors a participant's badge to the top-left of their tile (top-right
  // is Meet's own mute icon, bottom-left is the participant's name label —
  // top-left is the one corner Meet doesn't already use). Called on an
  // interval from content.js since Meet reflows tile layout whenever
  // participants join/leave or the window resizes.
  function positionBadge(key, targetEl) {
    if (!targetEl) return;
    const el = ensureBadge(key);
    const rect = targetEl.getBoundingClientRect();
    el.style.top = `${Math.max(0, rect.top + TILE_INSET_PX)}px`;
    el.style.left = `${Math.max(0, rect.left + TILE_INSET_PX)}px`;
  }

  window.__mithyax = window.__mithyax || {};
  window.__mithyax.ui = { ensureBadge, updateBadge, removeBadge, positionBadge };
})();
