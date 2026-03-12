# POLISH.md — Tool Use Notes from "Basecamp 5: Launch campaign" Setup

Notes on every Basecamp project tool exercised via the CLI, including what
didn't work on the first attempt and what the fix was (or wasn't).

## Tools Used Successfully on First Try

### Message Board
- `basecamp message` — worked perfectly. Markdown in the body converted to HTML
  automatically. Posted two messages: the launch plan overview and the launch day
  playbook.

### Chat (Campfire)
- `basecamp chat post` — worked on first try. Simple positional argument.

### To-dos (Todolists + Todos)
- `basecamp todolists create` — worked. Created three lists (pre-launch, launch
  day, post-launch).
- `basecamp todo` — worked. Created 13 todos across three lists with `--list`,
  `--due`, and `--in` flags. No issues.

### Docs & Files
- `basecamp files doc create` — worked. Created two documents (brand guidelines,
  launch email draft). Markdown body converted correctly.
- `basecamp files folder create` — worked. Created "Launch Assets" folder.

### Schedule
- `basecamp schedule create` — worked. Created four entries: all-day events and
  timed meetings. The `--all-day` flag combined with `--starts-at`/`--ends-at`
  behaved as expected.

### Card Table (Kanban)
- `basecamp cards column create` — worked. Created three columns (Tagline
  Candidates, In Progress, Final Pick). The card table was initially disabled
  in the project dock, but creating a column auto-enabled it.
- `basecamp card` — worked. Created three tagline candidate cards with HTML
  body content in the Tagline Candidates column using `--column <id>`.

### Comments
- `basecamp comment` — worked. Commented on the launch plan message using the
  message's recording ID. Markdown converted to HTML.

## Tools That Failed on First Try

### Automatic Check-ins
- **First attempt:** `basecamp checkins question create "..." --in 46453737`
  - **Error:** `questionnaire not found: 46453737` / `code: "not_found"` /
    `hint: "Questionnaire is disabled for this project"`
  - **What happened:** New projects have the Questionnaire tool disabled by
    default. The CLI can't enable disabled dock tools — that must be done in
    the Basecamp web UI.
- **Second attempt:** `basecamp checkins question create "..." --in 46453737 --questionnaire 9668356639`
  - **Error:** `validation error` / `code: "validation"`
  - **What happened:** Even passing the explicit questionnaire ID from the dock
    doesn't work. The Basecamp API rejects question creation against a disabled
    questionnaire. **There is no CLI workaround.** The tool must be enabled in
    the web UI first.
- **Fix needed:** The CLI could potentially expose a `basecamp projects enable-tool`
  command that toggles dock items via the API (if the API supports it), or at
  minimum surface a clearer error message like "Enable Automatic Check-ins in
  the Basecamp web UI before creating questions."

## Summary

| Tool | First-try success? | Notes |
|------|--------------------|-------|
| Message Board | Yes | Markdown body works great |
| Chat | Yes | Simple and clean |
| To-dos | Yes | Lists + todos, dates, all fine |
| Docs & Files | Yes | Documents + folders |
| Schedule | Yes | All-day and timed events |
| Card Table | Yes | Columns + cards, auto-enabled from disabled state |
| Comments | Yes | On messages, Markdown converted |
| Check-ins | **No** | Disabled by default on new projects; CLI cannot enable it |
