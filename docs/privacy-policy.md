# MithyaX Privacy Policy

*Last updated: 2026-09-01*

This policy covers the MithyaX Chrome extension for Google Meet.

## What MithyaX does

MithyaX analyzes the video and audio of other participants in a Google
Meet call you're on, in real time, to estimate whether what you're
seeing/hearing from them is likely authentic or synthetically
generated (a "deepfake"). It shows the result as a badge in the call
window.

## What data is captured

While you're on a Google Meet call and MithyaX is active, it captures
short video and audio samples of the **other participants** in that
call (not your own camera/microphone feed, and not the content of chat
or screen shares).

## Where that data goes

Captured samples are sent to a **MithyaX gateway** — a server operated
by whoever invited you to use MithyaX (your organization, or the pilot
you're participating in). You choose which gateway to connect to, by
entering its URL and an access token on the extension's Setup page.
MithyaX Inc./the extension developer does not operate a shared,
default gateway that all users' data flows through — each deployment
is separate.

The gateway analyzes the sample and returns a verdict, which the
extension displays. Depending on how the gateway is configured, it may
briefly log the verdict, a session identifier, and — if you use the
👍/👎 feedback control — whether you found that verdict useful. This
log is retained by whoever operates that gateway, under their own
retention practices.

## What MithyaX does not do

- It does not sell captured video/audio, or any derived data, to
  anyone.
- It does not use captured data for advertising, profiling, or any
  purpose other than producing the authenticity verdict shown to you.
- It does not run on any site other than `meet.google.com`.
- It does not access your browsing history, other tabs, or any data
  outside the Google Meet call it's actively watching.

## Other call participants

MithyaX analyzes people other than the extension's user. If you use
MithyaX, you're responsible for complying with the recording/consent
laws that apply in your jurisdiction and your organization's policies
— for example, letting other participants know MithyaX is active on
the call, where required.

## Data retention

Video/audio samples are processed transiently by the gateway to
produce a verdict and are not stored as video/audio after analysis.
Structured logs (session id, verdict, optional feedback) persist only
as long as the gateway operator retains their logs.

## Your choices

- MithyaX only activates on Google Meet and only after you complete
  Setup — uninstalling the extension or leaving the gateway
  URL/token unset stops all data capture.
- You can revoke the extension's access to your configured gateway at
  any time from `chrome://extensions`.

## Contact

Questions about this policy or MithyaX's data handling:
**vamshiproject02@gmail.com**
