// options.js — the extension's only configuration surface (Phase 8.10).
// Stores { gatewayUrl, extensionToken } in chrome.storage.local, which
// background.js reads fresh on every "start" (see its getConfig()) —
// this page never talks to background.js directly, storage is the only
// hand-off.
//
// Manifest V3 host_permissions are static at install time, so a gateway
// URL only known at runtime (a pilot tester's own server) can't be
// declared up front. manifest.json instead declares a broad
// optional_host_permissions, and this page requests the *specific*
// origin the user enters via chrome.permissions.request() — Chrome
// scopes the actual grant to that one origin, not the broader optional
// list. That request also happens to be exactly what makes fetch()
// below able to read the response body: an extension page fetching an
// origin it doesn't hold permission for is still subject to normal CORS,
// so "Test connection" and "Save" both request permission before
// fetching, not just before storing.

const urlInput = document.getElementById("gatewayUrl");
const tokenInput = document.getElementById("extensionToken");
const statusEl = document.getElementById("status");

function showStatus(kind, message) {
  statusEl.className = kind;
  statusEl.textContent = message;
}

function normalizeUrl(raw) {
  const trimmed = raw.trim().replace(/\/+$/, "");
  let url;
  try {
    url = new URL(trimmed);
  } catch {
    throw new Error("Enter a full URL, e.g. https://your-gateway.example.com");
  }
  if (url.protocol !== "http:" && url.protocol !== "https:") {
    throw new Error("Gateway URL must start with http:// or https://");
  }
  return trimmed;
}

async function requestOriginPermission(gatewayUrl) {
  const origin = `${new URL(gatewayUrl).origin}/*`;
  const granted = await chrome.permissions.request({ origins: [origin] });
  if (!granted) throw new Error("Permission for that address was declined — MithyaX can't connect without it.");
}

async function checkHealth(gatewayUrl) {
  let resp;
  try {
    resp = await fetch(`${gatewayUrl}/health`);
  } catch (err) {
    throw new Error(`Could not reach the gateway: ${err.message}`);
  }
  if (!resp.ok) throw new Error(`Gateway responded with HTTP ${resp.status}`);
}

async function load() {
  const { gatewayUrl, extensionToken } = await chrome.storage.local.get(["gatewayUrl", "extensionToken"]);
  if (gatewayUrl) urlInput.value = gatewayUrl;
  if (extensionToken) tokenInput.value = extensionToken;
}

document.getElementById("test").addEventListener("click", async () => {
  try {
    const gatewayUrl = normalizeUrl(urlInput.value);
    showStatus("ok", "Requesting permission…");
    await requestOriginPermission(gatewayUrl);
    showStatus("ok", "Checking connection…");
    await checkHealth(gatewayUrl);
    showStatus("ok", "✓ Connected — the gateway is reachable.");
  } catch (err) {
    showStatus("error", err.message);
  }
});

document.getElementById("save").addEventListener("click", async () => {
  try {
    const gatewayUrl = normalizeUrl(urlInput.value);
    const extensionToken = tokenInput.value.trim();
    if (!extensionToken) throw new Error("Enter the extension token you were given.");

    await requestOriginPermission(gatewayUrl);
    await chrome.storage.local.set({ gatewayUrl, extensionToken });
    showStatus("ok", "✓ Saved. Open Google Meet — MithyaX will connect automatically.");
  } catch (err) {
    showStatus("error", err.message);
  }
});

load();
