# Chrome Web Store listing — copy-paste content

Everything below goes into the Developer Dashboard when you submit
`mithyax-extension.zip` (rebuild it with `package.sh` first). Nothing
here needs engineering work — it's just text to paste into forms.

## Store listing tab

**Extension name**
`MithyaX for Google Meet` (matches `manifest.json` — CWS pulls this by
default). If you'd rather list it as plain `MithyaX`, that's a one-line
edit to `manifest.json`'s `name` field before packaging; either is fine.

**Short description** (max 132 characters — this one is 111)
```
Real-time deepfake risk detection for Google Meet — a live authenticity badge on every participant's video.
```

**Detailed description**
```
MithyaX watches the other participants in your Google Meet call and
gives you a live, per-person read on whether their video/audio looks
authentic — a small badge in the corner of the call showing Likely
Authentic, Suspicious, or Likely Fake, with a "Why?" breakdown behind
it.

How it works: MithyaX captures short video/audio samples of each
participant and sends them to a MithyaX gateway server — either one
your organization runs, or one your pilot organizer gave you — for
analysis. The verdict streams back in real time and updates as the
call continues.

This extension does nothing on its own — it requires a MithyaX gateway
URL and access token, which you'll be given by whoever invited you to
use MithyaX. See the Setup page (click the toolbar icon) to connect.

Currently supports Google Meet. Built for teams and individuals who
want a second opinion on who they're actually talking to.
```

**Category**: Productivity (or Communication — either fits; Productivity
tends to get less scrutiny for a first submission)

**Language**: English

## Privacy practices tab

**Single purpose description**
```
Analyzes the video and audio of other participants in a Google Meet
call, in real time, and displays a deepfake-likelihood indicator for
each — using a MithyaX gateway server the user configures themselves.
```

**Permission justifications** (one per permission CWS asks about)

| Permission | Justification |
|---|---|
| `storage` | Saves the user's MithyaX gateway URL and access token locally (chrome.storage) so they don't have to re-enter them every session. |
| `host_permissions: https://meet.google.com/*` | Required to inject the badge UI and read call video/audio on Google Meet pages. Scoped to that one origin — MithyaX cannot run on any other site. |
| `optional_host_permissions: http://*/*, https://*/*` | MithyaX gateways are self-hosted, each at a URL the user's organization chooses. This is requested as *optional* and Chrome only grants it for the exact origin the user enters on the Setup page — used solely to send captured frames to that one gateway and receive the verdict back. |

**Data usage disclosures** — when the dashboard asks "does your item
collect or use any of the following," answer:

- **Personally identifiable information** → Yes. Justification: video
  containing other call participants' faces is sent to the
  user-configured gateway for analysis.
- **Website content** → Yes (Meet call audio/video frames).
- Everything else (location, financial/health info, browsing history,
  keystrokes, etc.) → **No**.

Then affirm the required certifications:
- Data is **not sold** to third parties.
- Data is used **only** for the extension's stated single purpose
  (deepfake analysis), not for unrelated purposes, ads, or creditworthiness
  decisions.
- Data **is** transmitted over an encrypted connection — this is true
  only if every gateway URL testers use is `https://`. If your pilot
  gateway is currently plain `http://`, switch it to `https://` (even
  a self-signed/reverse-proxied cert) before submitting, and say so
  honestly here — don't check this box if it isn't true.

**Privacy policy URL**: paste the link once you've picked where to
host `privacy-policy.md` (see that file / the question below).

## Store assets checklist

- [x] 128×128 icon — `icons/icon128.png` already exists
- [ ] 3–5 screenshots, 1280×800 or 640×400 — capture these from a real
      Meet call (see docs/pilot.md's "before sending anything" checklist —
      do that real-world test and screenshot the badge in each state:
      Analyzing…, a Likely Authentic verdict, the "Why?" expansion, and
      the 👍/👎 feedback prompt)
- [ ] Small promo tile, 440×280 (optional, but listings with one get
      more clicks — low priority for an Unlisted pilot)

## Submission settings

- **Visibility: Unlisted** — not searchable, installable only by
  people you send the direct link to. This is the recommended setting
  until the 5→10 person validation in docs/pilot.md passes.
- **Distribution**: no geographic restriction needed for a pilot.
