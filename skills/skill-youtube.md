---
name: youtube
description: YouTube research — fetch transcripts, search videos/channels, browse playlists
command: youtube
---

# YouTube Research Skill

Fetch YouTube video transcripts, search videos/channels, browse channel uploads and playlists.

## API Details

- **Base URL:** `https://transcriptapi.com/api/v2`
- **Auth:** Bearer token in `Authorization` header
- **API Key:** `sk_F57kFJrvnC4mNniesEJwt45G0k2LMBoLlusXbpfoNtk`
- **Credits:** 1 per request (some endpoints free)

## Endpoints

### Get Transcript (1 credit)
```bash
curl -s "https://transcriptapi.com/api/v2/youtube/transcript?video_url=VIDEO_ID&format=text&include_timestamp=false&send_metadata=true" \
  -H "Authorization: Bearer API_KEY"
```

**Parameters:**
- `video_url` (required) — YouTube URL, short URL, or video ID
- `format` — `json` (default, segments with start/duration) or `text` (plain text)
- `include_timestamp` — `true`/`false` (default true). For text: `[123.45s] text`
- `send_metadata` — `true`/`false` (default false). Adds title, author, thumbnail

### Search Videos (1 credit)
```bash
curl -s "https://transcriptapi.com/api/v2/youtube/search?q=QUERY&type=video&limit=10" \
  -H "Authorization: Bearer API_KEY"
```

- `q` (required) — search query
- `type` — `video` or `channel`
- `limit` — 1-50 (default 20)

### Search Within Channel (1 credit)
```bash
curl -s "https://transcriptapi.com/api/v2/youtube/channel/search?channel=@HANDLE&q=QUERY&limit=10" \
  -H "Authorization: Bearer API_KEY"
```

### Channel Videos — Paginated (1 credit/page)
```bash
# First page
curl -s "https://transcriptapi.com/api/v2/youtube/channel/videos?channel=@HANDLE" \
  -H "Authorization: Bearer API_KEY"

# Next page (use continuation_token from response)
curl -s "https://transcriptapi.com/api/v2/youtube/channel/videos?continuation=TOKEN" \
  -H "Authorization: Bearer API_KEY"
```

### Channel Latest 15 (FREE)
```bash
curl -s "https://transcriptapi.com/api/v2/youtube/channel/latest?channel=@HANDLE" \
  -H "Authorization: Bearer API_KEY"
```

### Resolve Channel (FREE)
```bash
curl -s "https://transcriptapi.com/api/v2/youtube/channel/resolve?input=@HANDLE" \
  -H "Authorization: Bearer API_KEY"
```

### Playlist Videos — Paginated (1 credit/page)
```bash
curl -s "https://transcriptapi.com/api/v2/youtube/playlist/videos?playlist=PLAYLIST_ID" \
  -H "Authorization: Bearer API_KEY"
```

## Usage Notes

- Video IDs are 11 characters (e.g. `zwV5qC1wS6M`)
- Accepts full URLs, short URLs (`youtu.be/...`), or bare video IDs
- Channel endpoints accept `@handle`, channel URL, or `UC...` ID
- `format=text&include_timestamp=false` gives clean plain text (best for analysis)
- `format=text&include_timestamp=true` gives `[0.0s] text` format
- `format=json` gives segments with `text`, `start`, `duration` fields
- Always include `send_metadata=true` for video title/author context

## Agent Usage

When spawning a YouTube research sub-agent, include this in the task:
```
Use the TranscriptAPI to access YouTube content.
API Key: sk_F57kFJrvnC4mNniesEJwt45G0k2LMBoLlusXbpfoNtk
Base URL: https://transcriptapi.com/api/v2
Auth: -H "Authorization: Bearer API_KEY"
```
