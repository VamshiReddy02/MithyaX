# Installing MithyaX for Google Meet

This is a Chrome extension that flags likely deepfakes from the other
people in a Google Meet call. You don't need to know any programming to
install or use it.

## What you'll need

- Google Chrome (or another Chromium-based browser: Edge, Brave, etc.)
- The **gateway URL** and **extension token** from whoever invited you
  to test MithyaX — you'll paste these in during setup below.

## 1. Download

Get `mithyax-extension.zip` from wherever the person running this pilot
shared it with you, and unzip it. You should end up with a folder
containing files like `manifest.json`, `background.js`, and so on —
that whole folder is the extension.

## 2. Install

1. Open `chrome://extensions` in Chrome.
2. Turn on **Developer mode** (top-right toggle).
3. Click **Load unpacked**.
4. Select the unzipped folder from step 1.

MithyaX's icon (a dark square with an "M") should now appear in your
Chrome toolbar. If you don't see it, click the puzzle-piece icon in the
toolbar and pin MithyaX so it stays visible.

## 3. Configure/connect

1. Click the MithyaX toolbar icon. This opens the setup page.
2. Paste in the **Gateway URL** and **Extension Token** you were given.
3. Click **Test connection** — you should see "✓ Connected". Chrome may
   ask you to approve access to that one address; click Allow (this is
   what lets MithyaX talk only to that specific server, nothing else).
4. Click **Save**.

If "Test connection" fails, double-check the URL (no typos, no trailing
slash) and that the token was copied in full, then ask the pilot
organizer to confirm the gateway is running.

## 4. Open Google Meet

Join or start any Google Meet call at `meet.google.com`.

## 5. MithyaX works

Once another person's camera is on, a small badge appears in the
top-left of the call showing MithyaX's current read on them:

- **⚪ Connecting… / Analyzing…** — starting up, this is normal for the
  first few seconds.
- **🟢 / 🟡 / 🔴** — a real-time verdict (likely real / suspicious /
  likely fake). Click "Why?" under the badge for the reasoning behind
  it.
- **⚠️ Analysis unavailable** — MithyaX couldn't reach the gateway or
  lost the connection and gave up retrying. Nothing you did wrong;
  worth a quick check in with the pilot organizer if it persists.
- **⚙️ Setup needed** — you're seeing this because step 3 above hasn't
  been completed yet, or was interrupted. Click the toolbar icon to
  finish it. If you configure MithyaX *while already in a call*, leave
  and rejoin (or reload the Meet tab) — it only checks your settings
  when it first starts watching someone.

Once you see a verdict, you'll also see a small feedback prompt under
the badge — feel free to use it.

## Updating

If you're given a new version of the zip during the pilot: unzip it
over (or in place of) the old folder, then go to `chrome://extensions`
and click the refresh icon on the MithyaX card — or just reload it via
"Load unpacked" again pointing at the new folder. Your saved Gateway
URL/token aren't affected by this.

## Uninstalling

`chrome://extensions` → find MithyaX → **Remove**.
