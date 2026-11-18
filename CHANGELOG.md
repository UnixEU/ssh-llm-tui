# Changelog

## 1.2.0

- Uploads keep directories. `scp -r folder user@host:uploads/` stores each file under its relative path (`/uploads/<user>/folder/sub/file`). Works with the default SFTP-based `scp` and with the legacy protocol (`scp -O -r`), including a target that does not exist yet. `scp -r` downloads return the tree.
- SFTP: `mkdir`, listing any subdirectory, `stat`/`lstat` for files and directories, `rmdir` (whole tree), `rm` and `rename` of files and directories.
- Path checks per component: no `..`, absolute paths, empty components or control characters, at most 16 levels and 1024 bytes, and nothing is written through a symlink.
- New `UPLOAD_IGNORE` (default `.git,node_modules,.venv,__pycache__,.DS_Store`). Paths under those components are accepted, so a recursive upload of a Git checkout does not stop, and their data is discarded. One INFO log line per ignored directory.
- Higher limits: `UPLOAD_MAX_FILES` default 1000 (was 10). New `UPLOAD_MAX_TOTAL_BYTES` per user, default 200 MiB. `UPLOAD_MAX_BYTES` stays 10 MiB per file. A file over a limit is refused on its own with a log line naming the limit; nothing half written is stored. The compose tmpfs for `/uploads` is 512 MiB.
- New `UPLOAD_AUTO_INCLUDE` (default 20). New files are included automatically until that many are included or the token budget is reached; later files arrive excluded.
- File box: directory rows with included/total counts and tokens, space toggles a whole directory, `d` removes a directory tree, `g`/`G` jump to the top or bottom, and the title and summary show included tokens against the budget.
- The prompt labels files by relative path. Included files that do not fit the budget are named in a closing note. The attached-files list is capped at 100 names.
- SFTP sessions send an exit status, so `scp` in SFTP mode exits 0 after a successful upload instead of 1.

## 1.1.1

- `docker-compose.yml` sets fixed names: project `ssh-llm-tui`, container `ssh-llm-tui`, network `ssh-llm-tui-net`, volumes `ssh-llm-tui-hostkeys` and `ssh-llm-tui-fitdata`. They no longer depend on the checkout directory.
- Upgrading from an earlier compose file creates new volumes. The server host key is generated again, so SSH clients warn once about a changed host key; remove the old entry from `known_hosts`. Measured GPU usage starts empty. The old `<project>_hostkeys`, `<project>_fitdata` volumes and `<project>_default` network can be removed afterwards.

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
