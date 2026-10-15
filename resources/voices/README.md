# Starter voices

`*.asvoice` files here are embedded in the app (starter_voices.go) and
imported into a new library once, so a new user can make an audiobook or
a dub with a Base model (which speaks only with a saved voice) before they
clone or design one (thread #41, option A).

Rules for a file added here:

- Designed with **Qwen3-TTS 1.7B VoiceDesign** (Apache-2.0 output), not
  OmniVoice (CC BY-NC); no real person's voice.
- A clean sample of 8–12 s with no long pauses (weak samples drift when
  cloned: research/model-eval.md §6).
- Exported from AudioSmith (Voices → ⋯ → Export) without a consent
  recording; tagged `starter`.
- Its provenance (model, description, seed, date) is listed in NOTICE.
