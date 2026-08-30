// ui.js — the floating MithyaX badges overlaid on the Meet page, one per
// tracked remote participant (Phase 8.4). Purely presentational: it
// renders whatever state background.js/detector.js send down for a given
// participant key, it never computes a verdict itself.
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

  // One entry per participant key: { badgeEl, dotEl, verdictEl }. Was a
  // handful of module-level singletons before 8.4 — every function below
  // now takes a key to know which participant's badge it's touching, so
  // one participant's badge never affects another's.
  const badges = new Map();

  function ensureBadge(key) {
    const existing = badges.get(key);
    if (existing && document.body.contains(existing.badgeEl)) return existing.badgeEl;

    const badgeEl = document.createElement("div");
    // A class, not an id — ids must be unique per document, and 8.4
    // needs one badge per participant on screen at once.
    badgeEl.className = "mithyax-badge";

    const dotEl = document.createElement("span");
    dotEl.className = "mithyax-dot";
    dotEl.textContent = TIER_EMOJI.unknown;

    const label = document.createElement("span");
    label.className = "mithyax-label";
    label.textContent = "MithyaX";

    const verdictEl = document.createElement("span");
    verdictEl.className = "mithyax-verdict";
    verdictEl.textContent = "Connecting…";

    badgeEl.append(dotEl, label, verdictEl);
    document.body.appendChild(badgeEl);

    badges.set(key, { badgeEl, dotEl, verdictEl });
    return badgeEl;
  }

  function updateBadge(key, state) {
    ensureBadge(key);
    const { badgeEl, dotEl, verdictEl } = badges.get(key);
    const tier = state.tier || "unknown";
    dotEl.textContent = TIER_EMOJI[tier] || TIER_EMOJI.unknown;
    const pct = state.riskScore == null ? "" : ` ${Math.round(state.riskScore * 100)}%`;
    verdictEl.textContent = (state.label || "Analyzing") + pct;
    badgeEl.dataset.tier = tier;
    badgeEl.title = (state.reasons || []).join("\n") || "No risk signals yet";
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
