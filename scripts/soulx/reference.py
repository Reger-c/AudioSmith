"""Exports parity fixtures from the official SoulX-Podcast code (thread #80).

Developer tool, never shipped; CPU PyTorch in the venv of setup-dev.sh:

    python scripts/soulx/reference.py SOULX_SRC UPSTREAM_DIR OUT.safetensors \\
        --ref REF0.wav "its transcript" --ref REF1.wav "its transcript" \\
        --turn 0 "the first turn's text" [--s3tok-dir DIR] [--sampler-json OUT.json]

Runs upstream's own modules (soulxpodcast at 5ac9c0e, transformers 4.57.1,
s3tokenizer, onnxruntime CAM++) and saves, for `audiosmith-engine
--soulx-check`:
  ref{i}_a16, ref{i}_logmel, ref{i}_tokens, ref{i}_xvector    16 kHz path (normalized audio → whisper log-mel →
                                                             S3 tokens; kaldi fbank → CAM++)
  ref{i}_a24, ref{i}_mel24                                    24 kHz path (normalized → the flow's mel)
  prompt_ids, logits                                          the language model's input for the first turn and
                                                             its float32 logits at the last position
  proc_hist, proc_scores                                      the processors generate() hands SoulX's sampling
                                                             loop, captured from generate() itself, applied to
                                                             those logits after 5 "generated" tokens
  flow_*                                                      flow matching with fixed noise (prompt = ref 0)
  hift_wav                                                    HiFT on flow_mel without random phase or noise
--sampler-json writes small synthetic cases of the same processors for the
Swift unit tests (engine/Tests/AudioSmithEngineTests/Fixtures).
"""
import argparse, json, os, sys
import numpy as np
import torch


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("src")
    ap.add_argument("upstream")
    ap.add_argument("out")
    ap.add_argument("--ref", nargs=2, action="append", required=True)
    ap.add_argument("--turn", nargs=2, required=True)
    ap.add_argument("--s3tok-dir")
    ap.add_argument("--sampler-json")
    ap.add_argument("--flow-steps", type=int, default=15)
    a = ap.parse_args()
    sys.path.insert(0, a.src)
    torch.manual_seed(0)

    import onnxruntime, s3tokenizer, torchaudio
    import torchaudio.compliance.kaldi as kaldi
    from soulxpodcast.utils.audio import mel_spectrogram, audio_volume_normalize
    from soulxpodcast.utils.dataloader import SPK_DICT, TEXT_START, TEXT_END, AUDIO_START, TASK_PODCAST
    from soulxpodcast.utils.text import normalize_text
    from soulxpodcast.models.modules import flow as flow_mod
    from soulxpodcast.models.modules.hifigan import HiFTGenerator
    from safetensors.numpy import save_file

    OFFSET, EOS = 152927, 151675
    fx, info = {}, {"ref_texts": [t for _, t in a.ref], "turn_speaker": int(a.turn[0]), "turn_text": a.turn[1],
                    "flow_steps": a.flow_steps}

    tok = s3tokenizer.load_model("speech_tokenizer_v2_25hz", download_root=a.s3tok_dir).eval()
    sess = onnxruntime.InferenceSession(os.path.join(a.upstream, "campplus.onnx"), providers=["CPUExecutionProvider"])
    refs = []
    for i, (path, text) in enumerate(a.ref):
        # Upstream's dataloader, step by step.
        audio = s3tokenizer.load_audio(path, sr=16000)
        audio = torch.as_tensor(audio_volume_normalize(audio))
        log_mel = s3tokenizer.log_mel_spectrogram(audio)
        mels, lens = s3tokenizer.padding([log_mel])
        with torch.no_grad():
            codes, clens = tok.quantize(mels, lens)
        tokens = codes[0, :clens[0]].numpy().astype(np.int32)
        feat = kaldi.fbank(audio.unsqueeze(0), num_mel_bins=80, dither=0, sample_frequency=16000)
        feat = feat - feat.mean(dim=0, keepdim=True)
        xvec = sess.run(None, {sess.get_inputs()[0].name: feat.unsqueeze(0).numpy()})[0].flatten()
        a24, sr = torchaudio.load(path, backend="soundfile")
        a24 = torch.as_tensor(audio_volume_normalize(a24[0])).unsqueeze(0)
        if sr != 24000:
            a24 = torchaudio.transforms.Resample(orig_freq=sr, new_freq=24000)(a24)
        mel = mel_spectrogram(a24).transpose(1, 2).squeeze(0)
        if mel.shape[0] % 2:
            mel = mel[:-1]
        p = f"ref{i}_"
        fx[p + "a16"] = audio.numpy().astype(np.float32)
        fx[p + "logmel"] = log_mel.numpy().astype(np.float32)
        fx[p + "tokens"] = tokens
        fx[p + "xvector"] = xvec.astype(np.float32)
        fx[p + "a24"] = a24[0].numpy().astype(np.float32)
        fx[p + "mel24"] = mel.numpy().astype(np.float32)
        # forward_longform's alignment: two mel frames per token.
        mel_len = mel.shape[0]
        if len(tokens) * 2 > mel_len:
            tokens = tokens[: mel_len // 2]
        else:
            mel = mel[: len(tokens) * 2]
        refs.append((text, tokens, mel, xvec))
        print(f"ref{i}: {len(audio) / 16000:.2f} s, {len(fx[p + 'tokens'])} tokens, mel {tuple(fx[p + 'mel24'].shape)}")

    # The language model: the first turn's prompt, float32 logits.
    from transformers import AutoModelForCausalLM, AutoTokenizer, StoppingCriteriaList, EosTokenCriteria
    from transformers import RepetitionPenaltyLogitsProcessor
    tokenizer = AutoTokenizer.from_pretrained(a.upstream, use_fast=True)
    ids = []
    for i, (text, tokens, _, _) in enumerate(refs):
        pt = f"{SPK_DICT[i]}{TEXT_START}{normalize_text(text)}{TEXT_END}{AUDIO_START}"
        if i == 0:
            pt = TASK_PODCAST + pt
        ids += tokenizer.encode(pt) + [int(t) + OFFSET for t in tokens] + [EOS]
    spk = int(a.turn[0])
    ids += tokenizer.encode(f"{SPK_DICT[spk]}{TEXT_START}{normalize_text(a.turn[1])}{TEXT_END}{AUDIO_START}")
    fx["prompt_ids"] = np.array(ids, np.int32)
    model = AutoModelForCausalLM.from_pretrained(a.upstream, torch_dtype=torch.float32).eval()
    with torch.no_grad():
        logits = model(torch.tensor([ids])).logits[0, -1].float()
    fx["logits"] = logits.numpy()
    info["text_logit_max"] = float(logits[:OFFSET].max())
    info["speech_logit_max"] = float(max(logits[OFFSET:].max(), logits[EOS]))
    print(f"prompt {len(ids)} ids; best text logit {info['text_logit_max']:.2f}, best speech/EOS {info['speech_logit_max']:.2f}")

    # The processors generate() builds and hands SoulX's custom loop, captured.
    speech = logits[OFFSET:]
    top = (torch.argsort(speech, descending=True)[:4] + OFFSET).tolist()
    hist = [top[0], top[1], top[0], top[2], top[3]]
    captured = {}

    def capture(self, input_ids, logits_processor, stopping_criteria, generation_config, synced_gpus=False, streamer=None, **kw):
        captured["names"] = [type(p).__name__ for p in logits_processor]
        captured["scores"] = logits_processor(input_ids, logits[None].clone())
        return input_ids

    rep = RepetitionPenaltyLogitsProcessor(penalty=1.25, prompt_ignore_length=len(ids))
    model.generate(input_ids=torch.tensor([ids + hist]), do_sample=True, top_k=100, top_p=0.9, min_new_tokens=8,
                   max_new_tokens=3000, temperature=0.6, use_cache=True, custom_generate=capture,
                   stopping_criteria=StoppingCriteriaList([EosTokenCriteria(eos_token_id=EOS)]), logits_processor=[rep])
    print("generate() hands the loop:", captured["names"])
    info["processors"] = captured["names"]
    fx["proc_hist"] = np.array(hist, np.int32)
    fx["proc_scores"] = captured["scores"][0].float().numpy()
    del model

    if a.sampler_json:
        write_sampler_cases(a.sampler_json, captured["names"])

    # Flow matching with fixed noise: prompt = ref 0, "generated" = ref 1's first tokens.
    flow = flow_mod.CausalMaskedDiffWithXvec()
    flow.load_state_dict(torch.load(os.path.join(a.upstream, "flow.pt"), map_location="cpu", weights_only=True), strict=True)
    flow.eval()
    _, ptok, pmel, pxv = refs[0]
    gen = refs[1][1][:60]
    total = 2 * (len(ptok) + len(gen))
    noise = torch.randn(1, 80, total, generator=torch.Generator().manual_seed(7))
    saved = torch.randn_like
    torch.randn_like = lambda mu, **k: noise.to(mu.dtype)
    # the reference flow is fixed at 15 steps (flow.py); honour --flow-steps
    orig_forward = flow.decoder.forward
    flow.decoder.forward = lambda mu, mask, n_timesteps, **k: orig_forward(mu, mask, a.flow_steps, **k)
    try:
        with torch.no_grad():
            mels, mlens = flow(torch.tensor([list(ptok) + list(gen)]), torch.tensor([len(ptok) + len(gen)]),
                               torch.tensor(pmel)[None], torch.tensor([pmel.shape[0]]), torch.tensor(pxv)[None],
                               streaming=False, finalize=True)
    finally:
        torch.randn_like = saved
    gen_mel = mels[:, :, pmel.shape[0]:int(mlens[0])]
    fx["flow_prompt_tokens"] = np.array(ptok, np.int32)
    fx["flow_prompt_mel"] = pmel.numpy()[None].astype(np.float32)
    fx["flow_xvector"] = pxv[None].astype(np.float32)
    fx["flow_tokens"] = np.array(gen, np.int32)
    fx["flow_noise"] = noise.numpy().astype(np.float32)
    fx["flow_mel"] = gen_mel.numpy().astype(np.float32)
    print("flow mel", tuple(gen_mel.shape))

    # HiFT without random phase or noise.
    hift = HiFTGenerator()
    sd = torch.load(os.path.join(a.upstream, "hift.pt"), map_location="cpu", weights_only=True)
    hift.load_state_dict({k.replace("generator.", ""): v for k, v in sd.items()}, strict=True)
    hift.eval()
    saved_rand, saved_randn = torch.rand, torch.randn_like
    torch.rand = lambda *s, **k: torch.zeros(*s, **{x: y for x, y in k.items() if x in ("dtype", "device")})
    torch.randn_like = lambda t, **k: torch.zeros_like(t)
    try:
        with torch.no_grad():
            wav, _ = hift(speech_feat=gen_mel)
    finally:
        torch.rand, torch.randn_like = saved_rand, saved_randn
    fx["hift_wav"] = wav[0].numpy().astype(np.float32)
    print("hift", tuple(wav.shape))

    save_file({k: np.ascontiguousarray(v) for k, v in fx.items()}, a.out, metadata={"info": json.dumps(info, ensure_ascii=False)})
    print("wrote", a.out)


def write_sampler_cases(path, names):
    """Synthetic cases of the processors (vocabulary 300, EOS = 299) for the Swift tests."""
    from transformers import (MinLengthLogitsProcessor, MinNewTokensLengthLogitsProcessor, RepetitionPenaltyLogitsProcessor,
                              TemperatureLogitsWarper, TopKLogitsWarper, TopPLogitsWarper, LogitsProcessorList)
    # generate() turns min_new_tokens into min_length = input + 8 too: both mask EOS for the first 8 tokens.
    want = ["MinLengthLogitsProcessor", "MinNewTokensLengthLogitsProcessor", "RepetitionPenaltyLogitsProcessor",
            "TemperatureLogitsWarper", "TopKLogitsWarper", "TopPLogitsWarper"]
    assert names == want, f"generate() builds {names}, the Swift sampler follows {want}"
    g = torch.Generator().manual_seed(3)
    cases = []
    for n, hist in enumerate([[], [5, 9, 5, 120, 7], [3] * 4 + list(range(10, 22)), [298, 0, 1, 2, 3, 4, 5, 6, 7, 8]]):
        V, EOS, prompt = 300, 299, [1, 2, 3]
        logits = torch.randn(1, V, generator=g) * (2 + n)
        procs = LogitsProcessorList([MinLengthLogitsProcessor(len(prompt) + 8, EOS), MinNewTokensLengthLogitsProcessor(len(prompt), 8, EOS),
                                     RepetitionPenaltyLogitsProcessor(1.25, prompt_ignore_length=len(prompt)),
                                     TemperatureLogitsWarper(0.6), TopKLogitsWarper(100), TopPLogitsWarper(0.9)])
        out = procs(torch.tensor([prompt + hist]), logits.clone())[0]
        cases.append({"logits": logits[0].tolist(), "generated": hist,
                      "expected": [None if not np.isfinite(v) else v for v in out.tolist()]})
    json.dump({"source": "transformers 4.57.1 processors, scripts/soulx/reference.py", "eos": 299, "cases": cases},
              open(path, "w"))
    print("wrote", path)


if __name__ == "__main__":
    main()
