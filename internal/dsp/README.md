# internal/dsp — pure-Go audio DSP

Stdlib only, no cgo. Samples are float32 in [-1, 1], interleaved (`dsp.Buffer{Rate, Channels, Samples}`), the layout of `mygo/audio.Decoder`. Every package works at any sample rate and channel count; mono and stereo are what the tests cover. Results carry codes and numbers, never UI strings. Long-render paths stream.

| Package | What | Streaming |
|---|---|---|
| `dsp` | Buffer, dB helpers, `Remix` (mono↔n: copy / average), gains, fades (equal-power, linear), `Range` | — |
| `dsp/dsptest` | test signals: sines, noise, silence, clicks, syllable-like bursts | — |
| `dsp/wav` | read PCM 8/16/24/32 + float 32/64, EXTENSIBLE, odd chunks, streamed sizes, RF64; write PCM16/PCM24/Float32, 44-byte header, optional TPDF dither, optional RF64 upgrade (`Options.Large`); `Create` (atomic temp+rename, `Abort`), `WriteFile`, `WriteFileAtomic`, `Encode` | `Reader`, `Writer` |
| `dsp/resample` | polyphase Kaiser-windowed sinc; `Fast`/`Good`/`Best`; delay-compensated, exact output length `ceil(n·out/in)`; `Convert` = resample + remix | `Resampler.Process/Flush` |
| `dsp/filter` | RBJ biquads (HP, LP, BP, peak, shelves), `Chain`, BS.1770 `KWeighting(rate)`, Kaiser FIR design, `TruePeak` detector | `Chain`, `TruePeak` |
| `dsp/dynamics` | compressor (RMS/peak detector, soft knee, attack/release, makeup, linked); look-ahead brick-wall limiter (sample or true peak) | yes |
| `dsp/loudness` | BS.1770-4 / R128 meter: momentary, short-term, integrated (gated), LRA, sample + true peak, series; `Normalize` / `Verify` to `ACX`, `Podcast`, `Broadcast`; `Plan` + `Stream` for two-pass; `NoiseFloor`, `RMS` | `Meter`, `Stream` |
| `dsp/master` | high-pass → EQ → compressor → loudness/ceiling; presets `raw broadcast podcast warm bright cinematic` as data (table in package doc) | `Processor` + `loudness.Stream` |
| `dsp/edit` | `Concat`/`Joiner` (crossfade, default 50 ms equal-power, or gap; returns spans), `Silence`, `GapPolicy` (chunk/sentence/line/paragraph/chapter), `Trim`/`TrimRange`, `Pauses`, `LimitPauses` | `Joiner` |
| `dsp/qc` | "Check audio": codes `empty silent invalid_samples clipping silence_gap loudness_jump dc_offset low_level edge_silence click`, severities, seconds ranges, per-code cap | `Checker` |
| `dsp/refcheck` | voice references (#83): `Measure` (speech, SNR estimate, LUFS, clipping, edges, pauses, F0 spread, rate), hint codes `no_speech too_short very_noisy clipping mismatch noisy short flat slow`, grades, `Tidy` (edges, long pauses, −18 LUFS), `PickWindow`, `Pick` | — |
| `dsp/analysis` | energy `VAD`, `Onsets` (speech starts + spectral flux), YIN `Pitch` + `Stats`, `LoudnessContour`, `Nuclei`/`SpeakingRate`, `Analyze`/`Baseline`/`Compare` (Prosody Mirror codes `higher lower louder softer faster slower animated flat whispered`) | — |
| `dsp/mix` | timeline of tracks/clips (sample-accurate offsets, From/To, gain, fades, balance pan, auto resample/remix), mute, `Duck` (under key tracks' clips; merge gap, attack ramp before speech, release after), `Sidechain` | `Mixer.Render(dst, from)` |

## Accuracy evidence (all in the tests)

- **K-weighting**: at 48 kHz the derived coefficients equal BS.1770-4 Tables 1–2 to 1e-12. Other rates use the same analogue prototypes (bilinear, prewarped). At 16 kHz the 1 kHz gain is +0.05 dB off the 48 kHz figure.
- **EBU Tech 3341 cases**, synthesised from their descriptions: stereo 1 kHz sines at 48, 44.1 and 24 kHz.
  - Cases 1–5 (integrated −23.0 / −33.0 LUFS, both gates) pass at ±0.1 LU.
  - Case 9 (short-term constant −23 over 1.34 s/1.66 s alternation) and case 12 (momentary constant −23 over 0.18 s/0.22 s alternation) hold to ±0.1 LU in every window.
  - Case 10, adapted (3 s bursts off the 100 ms grid), gives S max −23 ±0.1.
  - True-peak cases 15–18 (fs/4, fs/6, fs/8 at −6 dBFS with crest-missing phases; sample peaks −6 to −9.03 dBFS) read **−6.00 dBTP** (spec: +0.2/−0.4).
  - Not reproduced: cases 7 and 8, and 3342 cases 5 and 6, which are authentic programme files; case 13, which needs momentary values at sample resolution (ours are every 100 ms).
- **EBU Tech 3342 LRA cases 1–4** (−20/−30, −20/−15, −40/−20, −50/−35/−20/−35/−50): 10.00, 5.00, 20.00 and 15.00 LU (spec ±1).
- **True-peak limiter**: output true peak −1.50 dBTP against a −1.5 ceiling at 24, 44.1 and 48 kHz, with 11 dB of reduction.
- **Normalisation**: ACX, podcast and broadcast presets, mono and stereo, inputs at −40/−10/−1 dBFS. Integrated lands within 0.15 LU of target and true peak at or under the ceiling. ACX's noise floor and RMS checks are exercised.
- **Resampler (Good)** against an ideal sine: −101 to −137 dB error for 24k↔44.1k↔48k, 16k→48k and 48k→8k. Alias rejection is 94–113 dB. Chunked streaming is bit-identical to one-shot.
- **WAV**: round trips within ½ LSB. mygo/audio decodes our files bit-identically. A fuzz target covers the reader.

## Speed (Ryzen 9950X, one core; Apple M-series is similar or faster)

| Workload | Time / speed |
|---|---|
| Normalise 15 s of 24 kHz mono speech | ~80 ms (~190× real time) |
| Check audio, 30 s of 24 kHz mono | 100 ms (300×) |
| R128 meter with true peak, 48 kHz stereo | 177× |
| R128 meter without true peak | 1500× |
| Resample 24k→48k stereo (Good) | 330× |
| Prosody `Analyze`, 10 s | 29 ms |
| Mixer: 10 min stereo 48 kHz, 200 dialogue clips plus a ducked music bed | 0.3 s |

True peak dominates the loudness work: 8× oversampling at 24 kHz. Frames that provably cannot raise the running maximum are skipped.

## Conventions worth knowing

- **Mono loudness.** Mono counts once, per BS.1770: it reads 3 LU under the same signal on both channels of a stereo file. To count it twice, as some podcast tools do, use `loudness.Options.DualMono` or `NormalizeOptions.DualMono`.
- **ACX preset.**
  - Target −19 LUFS, accepted −23…−18, true peak ≤ −3 dBTP.
  - Noise floor ≤ −60 dB RMS, measured on the quietest 500 ms.
  - Also the RMS window −23…−18 dB, ACX's own measure. Every check reports a code with its value, min and max.
- **`master.Apply` levels before compressing.** It brings the input to −20 LUFS first, so a preset compresses a quiet take as much as a loud one. The preset's loudness target (−16 to −20 LUFS) then sets the output level, unless the caller passes another `loudness.Target`.
- **Silence.** `Stats` reports silence as −120 (`loudness.Floor`), because JSON cannot carry −Inf.
