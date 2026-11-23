# ABR Lab — backend

Upload an MP4, get back an adaptive HLS stream (1080p / 720p / 480p / 360p / 144p) that an
Android player can switch between on the fly. Go + FFmpeg + S3.

```
 Android app                         Go server                                   S3
 ───────────                         ─────────                                   ──
 POST /videos (mp4) ───────────────▶ spool to disk ─────────────────────────────▶ raw/{id}/source.mp4
                                     queue job
 GET /videos/{id}/events  ◀── SSE ── worker: ffprobe → plan ladder
   step / progress / ready                   ffmpeg ×5 in parallel (aligned keyframes, 6 s chunks)
                                             measure chunks → master.m3u8
                                             upload ────────────────────────────▶ hls/{id}/master.m3u8
                                                                                  hls/{id}/720p/index.m3u8
 ExoPlayer(masterUrl) ─────────────▶ GET /hls/{id}/... ◀──── stream from S3 ───── hls/{id}/720p/seg_0003.ts
                                     (or straight from CloudFront)
```

## How it was built (one commit per phase)

| Phase | What it adds |
|---|---|
| 1. Foundation | Env config, JSON video index, storage interface (S3 + local folder), upload / list / get / delete |
| 2. FFmpeg pipeline | Worker queue, ffprobe, quality ladder, parallel per-rendition encode with aligned keyframes, HLS chunks, master playlist with measured bandwidth |
| 3. Publish & serve | Upload output to S3 (master last), `/hls/...` proxy or CDN URLs, `masterUrl` in responses, resume unfinished jobs after a restart |
| 4. Live progress | Server-Sent Events, cancel ffmpeg on delete, graceful shutdown, Docker + MinIO, end-to-end test |

## Run it

Needs Go 1.24+ and `ffmpeg`/`ffprobe` on your PATH (`brew install ffmpeg`).

```bash
make deps          # go mod tidy: fetches the AWS SDK, writes go.sum
```

**No AWS account (files on local disk):**

```bash
make run-local     # http://localhost:8080
```

**MinIO in Docker (S3 API, nothing to sign up for):**

```bash
make up            # API on :8080, MinIO console on :9001 (minioadmin/minioadmin)
```

**Real S3:**

```bash
export S3_BUCKET=abr-videos-yourname S3_REGION=ap-south-1
export AWS_ACCESS_KEY_ID=... AWS_SECRET_ACCESS_KEY=...   # or AWS_PROFILE=...
make run
```

The bucket stays private: by default the API streams HLS files to the
player itself. The IAM user or role needs only:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    { "Effect": "Allow", "Action": ["s3:PutObject", "s3:GetObject", "s3:DeleteObject"],
      "Resource": "arn:aws:s3:::abr-videos-yourname/*" },
    { "Effect": "Allow", "Action": "s3:ListBucket",
      "Resource": "arn:aws:s3:::abr-videos-yourname" }
  ]
}
```

For production, put CloudFront in front of the bucket (Origin Access Control)
and set `PUBLIC_HLS_BASE_URL=https://dxxxx.cloudfront.net`; `masterUrl` then
points at the CDN and chunks never pass through the API.

## API

### `POST /videos`
`multipart/form-data` with `file` (mp4, mov, m4v, mkv, webm) and optional `title`.
Returns **202** with the video (status `queued`). 413 over `MAX_UPLOAD_MB`, 415 for other file types.

```bash
curl -F "title=Goa sunset" -F "file=@sunset.mp4" localhost:8080/videos
```

### `GET /videos`
`{"videos": [...]}`, newest first — the Library screen.

### `GET /videos/{id}`

```json
{
  "id": "fab3295b54847602",
  "title": "Goa sunset",
  "status": "ready",
  "step": 6, "totalSteps": 6, "stepName": "Ready",
  "sizeBytes": 13159832,
  "source": { "width": 1920, "height": 1080, "fps": 30, "videoCodec": "hevc", "hasAudio": true, "durationSec": 168.2 },
  "renditions": [
    { "name": "1080p", "width": 1920, "height": 1080, "bandwidth": 5480000, "avgBandwidth": 5190000 },
    { "name": "720p",  "width": 1280, "height": 720,  "bandwidth": 3019280, "avgBandwidth": 3007785 },
    { "name": "480p",  "width": 854,  "height": 480,  "bandwidth": 1561653, "avgBandwidth": 1554384 }
  ],
  "progress": { "1080p": 100, "720p": 100, "480p": 100 },
  "masterUrl": "http://localhost:8080/hls/fab3295b54847602/master.m3u8",
  "thumbnailUrl": "http://localhost:8080/hls/fab3295b54847602/thumb.jpg",
  "createdAt": "2026-11-23T07:53:25Z"
}
```

`status` goes `queued → probing → transcoding → packaging → publishing → ready` (or `failed` with `error`).

| step | status | Processing screen |
|---|---|---|
| 1 | — | Uploaded |
| 2 | `queued` | Queued |
| 3 | `probing` | Inspecting source (ffprobe) |
| 4 | `transcoding` | Transcoding: per-rendition `progress` |
| 5 | `packaging` | Writing playlists |
| 6 | `publishing` | Publishing to storage |

### `GET /videos/{id}/events` — live progress (SSE)

```
event: snapshot   data: {...same as GET /videos/{id}...}
event: step       data: {"status":"transcoding","step":4,"totalSteps":6,"stepName":"Transcoding"}
event: progress   data: {"rendition":"720p","percent":61}
event: ready      data: {...video with masterUrl...}        ← stream ends
event: failed     data: {"error":"..."}                     ← stream ends
event: deleted    data: {}                                  ← stream ends
```

A `: ping` comment every 15 s keeps the connection open. On Android, OkHttp's
`okhttp-sse` reads this directly.

```bash
curl -N localhost:8080/videos/{id}/events
```

### `DELETE /videos/{id}` → 204
Stops a running encode, deletes `raw/{id}/` and `hls/{id}/` from storage, removes the record.

### `GET /hls/{id}/{path}`
HLS files from storage with the right `Content-Type` and cache headers. You
normally never build these URLs yourself — use `masterUrl`.

## What the pipeline does, and why

- **Ladder by short side.** "1080p" means a 1080-px short side, so a portrait reel
  (1080×1920) gets the same five qualities as landscape. Never upscales: a 720p upload gets 720p + 480p + 360p + 144p.
- **Aligned keyframes.** Every rendition gets a keyframe every `KEYFRAME_SECONDS`
  (`-force_key_frames`, `-sc_threshold 0`), so chunk N starts at the same moment in every quality
  and the player can switch at any chunk boundary without a glitch.
- **One ffmpeg per rendition, in parallel.** Gives per-quality progress for the UI; if one fails the others are stopped.
- **H.264 Main + AAC, 8-bit 4:2:0.** Plays on practically every Android device, even when the upload was 10-bit HEVC from an iPhone.
- **Measured bandwidth.** `BANDWIDTH` in master.m3u8 is the real peak chunk bitrate, which is what ExoPlayer's ABR compares against its speed estimate.
- **Master uploaded last.** A player can never see a master that points at chunks that are not uploaded yet.
- **Crash-safe.** Interrupted jobs are re-queued on startup and rebuilt from the stored original.

## Android quick notes

- Emulator → host: `http://10.0.2.2:8080`. `masterUrl` uses whatever host the app called, so it works unchanged.
- Plain `http://` needs a debug network security config (cleartext) on Android 9+.
- Player: Media3 ExoPlayer with the `media3-exoplayer-hls` module; `MediaItem.fromUri(masterUrl)` is all it needs.

## Configuration

| Variable | Default | |
|---|---|---|
| `STORAGE` | `s3` | `s3` or `local` |
| `S3_BUCKET` | — | required for `s3` |
| `S3_REGION` | `ap-south-1` (or `AWS_REGION`) | |
| `S3_ENDPOINT` / `S3_PATH_STYLE` | — / `false` | for MinIO / LocalStack |
| `PUBLIC_HLS_BASE_URL` | — | CDN base; empty = proxy through the API |
| `ADDR` | `:8080` | |
| `DATA_DIR` | `./data` | video index, upload spool, ffmpeg scratch |
| `LOCAL_STORAGE_DIR` | `$DATA_DIR/storage` | when `STORAGE=local` |
| `MAX_UPLOAD_MB` | `2048` | |
| `WORKERS` | `1` | videos encoded at once (each runs one ffmpeg per rendition) |
| `X264_PRESET` | `veryfast` | slower = smaller files, longer encodes |
| `SEGMENT_SECONDS` | `6` | chunk length; must be a multiple of `KEYFRAME_SECONDS` |
| `KEYFRAME_SECONDS` | `2` | |
| `FFMPEG_PATH` / `FFPROBE_PATH` | `ffmpeg` / `ffprobe` | |

## Tests

```bash
make test           # unit tests + an end-to-end run with real ffmpeg
make test-offline   # same, without compiling the AWS SDK
```

The end-to-end test generates a clip, uploads it, follows the SSE stream to
`ready`, fetches the master, a media playlist, a chunk and the thumbnail, then deletes it.

## Next steps

- SQLite/Postgres instead of the JSON index; Redis + a job queue for several API instances
- Presigned S3 uploads so big files go straight from the phone to S3
- fMP4 chunks (`.m4s`) for HEVC/AV1 renditions alongside H.264
- `POST /videos/{id}/playback-stats` for quality-switch and stall analytics
