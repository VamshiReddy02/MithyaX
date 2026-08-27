// ui.js — the floating MithyaX badge overlaid on the Meet page. Purely
// presentational: it renders whatever state background.js/detector.js
// send down, it never computes a verdict itself.
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

  let badgeEl = null;
  let dotEl = null;
  let verdictEl = null;

  function ensureBadge() {
    if (badgeEl && document.body.contains(badgeEl)) return badgeEl;

    badgeEl = document.createElement("div");
    badgeEl.id = "mithyax-badge";

    dotEl = document.createElement("span");
    dotEl.className = "mithyax-dot";
    dotEl.textContent = TIER_EMOJI.unknown;

    const label = document.createElement("span");
    label.className = "mithyax-label";
    label.textContent = "MithyaX";

    verdictEl = document.createElement("span");
    verdictEl.className = "mithyax-verdict";
    verdictEl.textContent = "Connecting…";

    badgeEl.append(dotEl, label, verdictEl);
    document.body.appendChild(badgeEl);
    return badgeEl;
  }

  function updateBadge(state) {
    ensureBadge();
    const tier = state.tier || "unknown";
    dotEl.textContent = TIER_EMOJI[tier] || TIER_EMOJI.unknown;
    const pct = state.riskScore == null ? "" : ` ${Math.round(state.riskScore * 100)}%`;
    verdictEl.textContent = (state.label || "Analyzing") + pct;
    badgeEl.dataset.tier = tier;
    badgeEl.title = (state.reasons || []).join("\n") || "No risk signals yet";
  }

  function removeBadge() {
    if (badgeEl) badgeEl.remove();
    badgeEl = null;
    dotEl = null;
    verdictEl = null;
  }

  const TILE_INSET_PX = 12;

  // Anchors the badge to the top-left of the given tile (top-right is
  // Meet's own mute icon, bottom-left is the participant's name label —
  // top-left is the one corner Meet doesn't already use). Called on an
  // interval from content.js since Meet reflows tile layout whenever
  // participants join/leave or the window resizes.
  function positionBadge(targetEl) {
    if (!targetEl) return;
    const el = ensureBadge();
    const rect = targetEl.getBoundingClientRect();
    el.style.top = `${Math.max(0, rect.top + TILE_INSET_PX)}px`;
    el.style.left = `${Math.max(0, rect.left + TILE_INSET_PX)}px`;
  }

  window.__mithyax = window.__mithyax || {};
  window.__mithyax.ui = { ensureBadge, updateBadge, removeBadge, positionBadge };
})();
