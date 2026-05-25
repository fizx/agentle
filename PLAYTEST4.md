- After completing a form or chat, the script result still shows suspended.
- ui_chat seems to be magically echo, not a configurable thing.
- please add a tab for "apps", that's designed to make it easy to run scripts with UI (assumes basic input event will lead to a meaningful ui), some scripts tagged as apps
- when in app mode, please make the chat and/or ui really nice.

---

> **Status (2026-05-24): all four addressed.**

- **Stale "suspended".** The Scripts editor captured the Execution once at run
  time, so a UI run stayed "suspended" forever even after you finished the
  chat/form. The Result card now polls `GET /executions/{id}` while the run is
  non-terminal, so the badge flips to completed/failed and the output appears as
  soon as the session ends. (`web/src/views/Scripts.tsx`)
- **ui_chat isn't echo.** `ui_chat()` only renders the panel — the script drives
  the conversation. The old `chat_ui` example happened to uppercase-echo, which
  made it look built-in. It's now an **LLM-backed assistant** with a configurable
  system prompt (`data.system`), and the source says so. (`internal/examples`)
- **Apps tab.** New `GET /api/apps` auto-detects apps (scripts whose latest
  version calls `ui_chat`/`ui_form`) and a new **Apps** tab (the default landing)
  lists them as one-click launchers — no editor, no JSON. (`internal/api`,
  `web/src/views/Apps.tsx`)
- **Nice app mode.** `UIPanel` gained a full-bleed "app mode": a header with a
  live status pill, larger/softer chat bubbles, an animated "thinking" indicator
  while the script works, a sticky composer, and a centered form card.
  (`web/src/components/UIPanel.tsx`, `web/src/style.css`)