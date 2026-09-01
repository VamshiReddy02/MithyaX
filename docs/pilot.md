# MithyaX Pilot Runbook (Phase 8.12)

8.11 built the one feedback mechanism (👍/👎) this needed. This phase
is the actual pilot itself — the question moves from "can we build
this" to "do people actually want this" — and it's run by you, not by
further engineering work. Nothing in this doc is something to send to
testers; what they get is `integrations/google-meet/extension/INSTALL.md`
and `mithyax-extension.zip` (rebuild it with `package.sh` first if
either the extension or this doc's instructions have changed since the
last build).

**Start with 5 people. Don't go to 10, 25, or 50 until the section at
the bottom says you're ready.**

## The flow

```
You
 ↓ send INSTALL.md + mithyax-extension.zip
Tester installs MithyaX
 ↓
Tester configures gateway (Options page)
 ↓
Tester opens Google Meet
 ↓
Tester calls another person
 ↓
MithyaX analyzes the participant
 ↓
Tester uses the result (sees a verdict, optionally gives 👍/👎 on the
badge itself — Phase 8.11's one feedback mechanism)
 ↓
Tester gives you feedback (the questions below)
```

Do the install/setup step **watching, but not helping** unless they're
fully stuck — the first, most important data point is whether they can
do it alone at all.

## Tracker

One row per tester. Keep it as plain as this — a spreadsheet, a note,
whatever. Don't build tooling for 5 rows.

| Tester | Install date | Installed without your help? | Configured without your help? | Notes |
|---|---|---|---|---|
| | | | | |

## What to ask

Ask these after their call, in order, in your own words — don't read
them verbatim like a script, and don't explain what the tiers mean
before asking the UX question. The point of asking cold is to find out
whether the product itself communicates that, not whether you can
explain it well.

### Installation
- "Walk me through installing it — where, if anywhere, did you get
  stuck?"
- "Did the setup step make sense before you tried it, or only after?"

### Detection
- "What did MithyaX show during your call?"
- "Did that match what you'd expect, given who you were actually
  talking to?"

### UX
- "In your own words, what does [whichever tier they saw] mean?" —
  asked before you explain anything.
- "Did you notice the 👍/👎 under the badge? What did you think it was
  for?"

### Reliability
- "Did anything crash, freeze, or disconnect?"
- "Was the badge ever on the wrong person, or missing when someone's
  video was clearly on?"
- "Any audio or video problems on your end during the call?"

### Product demand (the two that matter most — ask last, ask exactly
this, no lead-in)
- "Would you actually use MithyaX in your future video calls?"
- "Would you recommend this to someone who needs protection against
  AI/deepfake impersonation?"

Write down their answer before reacting to it. A "yes, I guess" is not
the same as a "yes" — note the hedge, don't smooth it over.

## The 👍/👎 signal

Every tester's click is one structured log line on your gateway. Pull
them all with:

```
docker compose -f deployments/docker/docker-compose.yml logs gateway | grep "detection feedback"
```

Each line has a `session_id` and `useful: true/false`. That's the
entire mechanism for this pilot, on purpose — don't build a dashboard
for 5 people's clicks.

## Before sending anything

- [ ] `package.sh` re-run since the last code change, so the zip
      matches what you're about to test.
- [ ] You've clicked through install → configure → a real Meet call →
      a real 👍/👎 click yourself, once, on your own machine.
- [ ] The gateway you're pointing testers at is the one you intend to
      keep running for the whole pilot — don't make them reconfigure
      mid-pilot if you can avoid it.

## Deciding 5 → 10

Don't expand the pilot until:
- Everyone in the first 5 installed and configured with either no help
  or only minor help (not "I had to do it for them").
- The two demand questions come back genuinely positive, not
  hedged-yes, from most of the 5.
- No unresolved crash/reliability report from this round.

If any of those aren't true, fix what's broken and run another round
of 5 before going wider — don't scale past a problem.
