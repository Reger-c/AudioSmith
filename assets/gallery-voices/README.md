# Curated voices' bundled clips

Discover voices' designed voices (internal/gallery/data/curated.json) ship
two clips each, Ogg Vorbis, mono 24 kHz:

- `<id>.ref.ogg`: the voice's reference (10–15 s), the best of several
  takes on Qwen3-TTS 1.7B VoiceDesign that says its text (Qwen3-ASR);
  "Use voice" saves it as the voice's reference, with the text in
  curated.json.
- `<id>.preview.ogg`: the voice's preview line, cloned from that reference
  on Qwen3-TTS 1.7B Base, as the Studio would make it.

Made by `scripts/make-curated-voices.sh` (thread #94). Generated speech of
no real person; the models are Apache-2.0 (see NOTICE).
