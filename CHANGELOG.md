# Changelog

## 1.1.0

- Memory check before a model loads. The app estimates the memory a model needs (weights, KV cache for the chosen context, recurrent state of hybrid models, compute buffers, runtime overhead, plus 3 %; token embeddings stay in RAM as llama.cpp keeps them there) and compares it with the memory free on each GPU, or in RAM for the CPU target. A model that does not fit is not loaded. The screen shows the numbers per device and offers the largest context that fits, the CPU, unloading idle models, or another model.
- Multi-GPU aware: with several GPUs the check splits the model as llama.cpp does and requires every GPU to fit its share, not just the total.
- Context size picker (8k, 16k, 32k, 64k, 128k by default, capped by the model's trained context). The file context budget follows the chosen size.
- Target picker: all GPUs, a single GPU, or the CPU. Settings are sent through DMR's configure API (`POST /engines/_configure`, the same as `docker model configure`). A busy model (HTTP 409) gets a clear message instead of a failed load.
- Optional read-only mount of DMR's model store (`docker-model-runner-models`) for exact sizes from GGUF headers. Without it the check uses GPU memory measured on earlier loads, and asks before loading a model of unknown size (`FIT_CHECK=strict` refuses).
- `ctrl+o` in the chat changes context size or placement for the current model.
- New settings: `FIT_CHECK`, `FIT_MARGIN_MIB`, `FIT_MARGIN_PCT`, `CONTEXT_CHOICES`, `DEFAULT_CONTEXT`, `CONTEXT_SELECT`, `TARGET_SELECT`, `ALLOW_CPU`, `MODEL_STORE_PATH`, `KV_CACHE_TYPE`, `CPU_THREADS`, `FIT_USAGE_PATH`.
- The GPU poller also reads `memory.free`.

## 1.0.2

- New default GPU colour bands. Temperature: green below 40 °C, yellow from 40, orange from 60, red from 70. VRAM: green below 40 %, yellow from 40, orange from 60, red from 90. Power: green below 25 W, yellow from 25, orange from 100, red from 200. All bands can still be set through the `GPU_*` variables.
- The GPU name uses the terminal's default text colour instead of blue, so only the values change colour.

## 1.0.1

- Clients that pipeline concurrent SFTP writes, such as OpenSSH sftp, could store a truncated or corrupt upload. The upload buffer is locked and grows by doubling. SCP already reads one file as a single stream and is unchanged.
- The focused file box shows the selected file's note, including an image decode error or a skipped binary. A failed image decode is logged at WARN with the error text.

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
