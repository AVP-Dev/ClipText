<div align="center">

<h1>ClipText</h1>

<h3>YouTube-to-Script · Local Whisper · OpenAI-Compatible APIs</h3>

<p>Go · Zero Dependencies · Embedded Web UI · Chrome Extension</p>

</div>

---

Minimal stack with zero third-party Go dependencies (`stdlib` + `//go:embed`), smart subtitle-first pipeline, local speech recognition via `whisper.cpp` (`whisper-cli`), and OpenAI-compatible APIs (**Groq**, **NVIDIA NIM**, **OpenAI**, **Custom**).

---

## Features

- **Subtitle-First Pipeline:** Instantly extracts manual or auto-generated YouTube subtitles (`VTT`) directly from metadata with rolling-caption deduplication.
- **Local Whisper (`whisper-cli`):** Automatically detects existing `ggml-*.bin` models on your machine (e.g. `~/.cache/whisper.cpp/`) and lets you download models directly from the UI.
- **OpenAI-Compatible APIs:** Built-in presets for **Groq** (`whisper-large-v3-turbo`), **NVIDIA NIM**, **OpenAI**, and **Custom** (manual Base URL, API Key, and model name).
- **Optional AI Post-Processing:** Clean up punctuation, format paragraphs, or generate a structured summary using any OpenAI-compatible `/chat/completions` endpoint (Groq, NVIDIA, Ollama, OpenAI).
- **Browser Extension (Chrome / Chromium):** Toolbar popup widget ([`extension/`](extension/)) that grabs the active YouTube tab URL, runs transcription in one click, copies/downloads text, and syncs YouTube cookies to your local server.
- **Export:** View plain text, timestamped segments, `SRT`, copy to clipboard, or download `.txt` / `.srt`.

---

## Requirements

- **Go 1.26+** (to build from source)
- **[yt-dlp](https://github.com/yt-dlp/yt-dlp)** — for video metadata, subtitles, and audio streams
- **[ffmpeg](https://ffmpeg.org/)** — for audio conversion (when subtitles are unavailable)
- **[whisper.cpp](https://github.com/ggml-org/whisper.cpp)** (`whisper-cli`) — for local offline transcription (optional if you only use subtitles or APIs)

---

## Quick Start

```bash
git clone https://github.com/AVP-Dev/ClipText.git
cd ClipText
go build -o cliptext .
./cliptext
```

The web interface opens at **`http://localhost:8080`**.

Useful flags:
```bash
./cliptext -addr :9090 -open=false
```

---

## Browser Extension (Chrome / Brave / Arc / Edge)

1. Open `chrome://extensions` (or `brave://extensions`, `edge://extensions`).
2. Enable **Developer mode**.
3. Click **Load unpacked** and select the `extension/` folder from this repository.
4. Start `./cliptext`, open any YouTube video, click the **ClipText** icon in your toolbar, and press **▶ Get Script**.

> **Tip (YouTube Anti-Bot):** If YouTube asks to confirm you're not a bot, open **Settings** inside the extension popup and click **Sync YouTube Cookies** — it sends your active YouTube session to the local server in one click.

---

## Privacy & Security

- All settings, API keys, and `cookies.txt` are stored **locally only** in your OS user config directory (`~/.config/cliptext/` or `~/Library/Application Support/cliptext/` on macOS) with `0600` permissions.
- `.gitignore` excludes all secrets, local configs, `cookies.txt`, temporary media files, and `.bin` models.

---

<div align="center">
<sub>AVPdev · Software Development &amp; Automation · <a href="https://avpdev.com/">avpdev.com</a></sub>
</div>
