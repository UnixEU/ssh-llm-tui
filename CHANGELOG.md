# Changelog

## 1.0.0

First public release. An SSH terminal UI for local models served by Docker Model Runner.

- Welcome screen, model picker, GPU warm-up, and streaming chat.
- Reasoning stays collapsed (`ctrl+t` expands it), including `<think>` tags.
- Finished assistant replies render as Markdown. `MARKDOWN=off` or `ctrl+r` shows the raw text.
- SCP and SFTP uploads on the same port and auth. Text, PDF, and DOCX can be included in the prompt. PNG, JPEG, WebP, and GIF are sent once as image parts when the model accepts them.
- GPU panel with per-device name, VRAM percent, temperature, and power, coloured by configurable bands.
- ASCII welcome banner, read on every welcome screen.
- `AUTH_MODE=open` or `authorized_keys`.
- The last session to exit unloads DMR runners. `ctrl+q` closes the session. `esc` on chat returns to the welcome screen.
