"""Fixtures from the official MOSS-TTSD code for the Swift port's parity checks (thread #82).

Developer tool, never shipped. Run it in the venv of scripts/mossttsd/setup-dev.sh
(parity.sh runs it):

    python reference.py tok   DEV CONVERTED OUT.safetensors --ref a.wav TEXT --ref b.wav TEXT
    python reference.py cases DEV SAMPLER.json TINY_DIR
    python reference.py ttsd  DEV CONVERTED TOK_FIXTURES OUT.safetensors --turn S TEXT ...

DEV is setup-dev.sh's folder: upstream/tok and upstream/ttsd (OpenMOSS's
repos at the pinned revisions; tok needs only ttsd's code and tokenizer
files), src/moss-tts (the Space's text recipe). Everything runs on the
CPU in float32, with the official modules:

  tok    The official processor (AutoProcessor, trust_remote_code) encodes
         each reference and their concatenation (encode_audios_from_wav:
         −20 dBFS loudness, batch_encode, first 16 codebooks) and decodes
         codes (decode_audio_codes: 8-second streaming chunks). Then again
         with the converted 8-bit codec's weights dequantized into the
         same model ("q8.*": the Swift 8-bit codec should match these to
         float rounding).
  cases  The official sampling functions (inference_utils.py) on random
         logits: the processed distributions the Swift sampler must give.
         And a tiny random MossTTSDelayModel (2 layers, hidden 64) run
         through the official generate() greedily (temperature 0, the
         repetition penalty on), weights and rows saved: the Swift delay
         loop must write the same rows.
  ttsd   The official processor (tokenizer, the Space's normalize_text
         recipe) builds the continuation prompt from the tok fixtures'
         codes; the official model's modules (Qwen3DecoderLayer etc.,
         eager attention) run it layer by layer (one layer in memory at a
         time) with upstream's bfloat16 weights in float32, and again with
         the converted 8-bit weights dequantized ("q8.*"). Logits of the
         17 heads at the last 25 positions (the prompt's end and 24
         teacher-forced audio rows).
"""
import argparse, ast, json, os, re, sys

import numpy as np
import torch

torch.set_grad_enabled(False)
N_VQ = 16


def load_wav(path):
    import soundfile as sf
    a, sr = sf.read(path, dtype="float32", always_2d=True)
    w = torch.from_numpy(a.mean(axis=1)).unsqueeze(0)
    if sr != 24000:
        import torchaudio
        w = torchaudio.functional.resample(w, sr, 24000)
    return w


def processor(dev, with_codec):
    from transformers import AutoProcessor
    kw = {"codec_path": os.path.join(dev, "upstream/tok")}
    if with_codec:
        return AutoProcessor.from_pretrained(os.path.join(dev, "upstream/ttsd"), trust_remote_code=True, **kw)
    # Tokenizer only: the class from the repo's code, without the codec.
    from transformers import AutoConfig, AutoTokenizer
    from transformers.dynamic_module_utils import get_class_from_dynamic_module
    d = os.path.join(dev, "upstream/ttsd")
    cls = get_class_from_dynamic_module("processing_moss_tts.MossTTSDelayProcessor", d)
    p = cls.__new__(cls)
    tok = AutoTokenizer.from_pretrained(d)
    cfg = AutoConfig.from_pretrained(d, trust_remote_code=True)
    p.tokenizer, p.audio_tokenizer, p.model_config = tok, None, cfg
    p.imstart_token_id = tok.convert_tokens_to_ids("<|im_start|>")
    p.imend_token_id = tok.convert_tokens_to_ids("<|im_end|>")
    p.newline_token_id = 198
    name = lambda i: tok.convert_ids_to_tokens(int(i))
    p.audio_user_slot_token = name(cfg.audio_user_slot_token_id)
    p.audio_assistant_gen_slot_token = name(cfg.audio_assistant_gen_slot_token_id)
    p.audio_assistant_delay_slot_token = name(cfg.audio_assistant_delay_slot_token_id)
    p.audio_start_token = name(cfg.audio_start_token_id)
    p.audio_end_token = name(cfg.audio_end_token_id)
    return p


def mlx_dequantize(path_or_arrays, key):
    import mlx.core as mx
    a = path_or_arrays
    if (key[: -len("weight")] + "scales") not in a:
        return np.array(a[key].astype(mx.float32))
    base = key[: -len("weight")]
    # In float32: dequantizing with bfloat16 scales would round the weights.
    w = mx.dequantize(a[key], a[base + "scales"].astype(mx.float32), a[base + "biases"].astype(mx.float32), group_size=64, bits=8)
    return np.array(w.astype(mx.float32))


# ---------------------------------------------------------------- tok

def step_tok(a):
    proc = processor(a.dev, True)
    codec = proc.audio_tokenizer.eval()
    wavs = [load_wav(p) for p, _ in a.ref]
    out = {}
    for i, w in enumerate(wavs):
        out[f"wav{i}"] = w[0].numpy()
        out[f"norm{i}"] = proc.loudness_normalize(w[0]).numpy()

    def run(prefix):
        codes = proc.encode_audios_from_wav(wavs, sampling_rate=24000)
        for i, c in enumerate(codes):
            out[f"{prefix}codes{i}"] = c[:, :N_VQ].numpy().astype(np.int32)
        cat = proc.encode_audios_from_wav([torch.cat(wavs, dim=-1)], sampling_rate=24000)[0][:, :N_VQ]
        out[f"{prefix}codesCat"] = cat.numpy().astype(np.int32)
        out[f"{prefix}decodedCat"] = proc.decode_audio_codes([cat])[0].numpy()
        out[f"{prefix}decoded0"] = proc.decode_audio_codes([codes[0][:, :N_VQ]])[0].numpy()
        enc = codec.batch_encode([proc.loudness_normalize(wavs[0][0])], num_quantizers=N_VQ)
        out[f"{prefix}hidden0"] = enc.encoder_hidden_states[0].numpy()
        print(prefix or "fp32", "codes", [c.shape[0] for c in codes], "cat", cat.shape[0],
              "decodedCat", out[f"{prefix}decodedCat"].shape[0] / 24000, "s")

    run("")
    # The converted 8-bit codec's weights, dequantized, in the official model.
    import mlx.core as mx
    q = mx.load(os.path.join(a.converted, "codec.safetensors"))
    sd = codec.state_dict()
    n = 0
    for k in q:
        if not k.endswith(".weight") or (k[: -len("weight")] + "scales") not in q:
            continue
        ok = k.replace(".self_attn.in_proj.", ".self_attn.in_projs.0.").replace(".self_attn.out_proj.", ".self_attn.out_projs.0.")
        w = torch.from_numpy(mlx_dequantize(q, k))
        assert sd[ok].shape == w.shape, (ok, sd[ok].shape, w.shape)
        sd[ok].copy_(w)
        n += 1
    print("dequantized", n, "linears")
    run("q8.")
    out["refText"] = np.frombuffer("\x00".join(t for _, t in a.ref).encode(), dtype=np.uint8)
    from safetensors.numpy import save_file
    save_file({k: np.ascontiguousarray(v) for k, v in out.items()}, a.out)
    print("wrote", a.out)


# ---------------------------------------------------------------- cases

def step_cases(a):
    from transformers.dynamic_module_utils import get_class_from_dynamic_module
    d = os.path.join(a.dev, "upstream/ttsd")
    sys.path.insert(0, d)
    import inference_utils as iu  # the repo's own sampling functions
    g = torch.Generator().manual_seed(82)
    cases = []

    def processed(logits, prev, penalty, top_k, top_p):
        x = iu.apply_repetition_penalty_delay_pattern(logits.clone(), prev, penalty) if prev is not None else logits.clone()
        x = iu.apply_top_k(x, top_k)
        x = iu.apply_top_p_optimized(x, top_p)
        return torch.softmax(x, dim=-1)

    for n, (rows, temp) in enumerate([(1, 1.1), (3, 1.1), (2, 0.7), (1, 1.5)]):
        logits = (torch.randn(rows, 1025, generator=g) * 3).round(decimals=3)
        prev = torch.randint(0, 1025, (40 + 10 * n, 1 if rows == 1 else 15), generator=g)
        x = logits / temp
        x[..., 1024] = float("-inf")
        p = processed(x, prev, 1.1, 50, 0.9)
        cases.append({"kind": "audio", "logits": logits.flatten().tolist(), "rows": rows, "temperature": temp,
                      "seen": sorted(set(prev.flatten().tolist())), "penalty": 1.1, "topK": 50, "topP": 0.9,
                      "expected": [{str(i): round(float(v), 7) for i, v in enumerate(r) if v > 0} for r in p]})
    # Text channel: no penalty; a vocabulary of 3000 with excluded ids.
    for n, keep in enumerate([None, [2001, 2005]]):
        logits = (torch.randn(1, 3000, generator=g) * 4).round(decimals=3)
        x = logits / 1.1
        if keep is None:
            x[..., [5, 7, 2999]] = float("-inf")
        else:
            mask = torch.ones(3000, dtype=torch.bool)
            mask[keep] = False
            x = x.masked_fill(mask, float("-inf"))
        p = processed(x, None, 1.0, 50, 0.9)
        cases.append({"kind": "text", "logits": logits.flatten().tolist(), "rows": 1, "temperature": 1.1,
                      "exclude": [5, 7, 2999] if keep is None else None, "keep": keep, "topK": 50, "topP": 0.9,
                      "expected": [{str(i): round(float(v), 7) for i, v in enumerate(r) if v > 0} for r in p]})
    json.dump({"cases": cases}, open(a.sampler_json, "w"))
    print("wrote", a.sampler_json, len(cases), "cases")

    # A tiny random model through the official generate(), greedy.
    from transformers import AutoConfig
    cfg_cls = get_class_from_dynamic_module("configuration_moss_tts.MossTTSDelayConfig", d)
    model_cls = get_class_from_dynamic_module("modeling_moss_tts.MossTTSDelayModel", d)
    base = json.load(open(os.path.join(d, "config.json")))
    lc = dict(base["language_config"])
    lc.update(hidden_size=64, intermediate_size=128, num_hidden_layers=2, num_attention_heads=4,
              num_key_value_heads=2, head_dim=16, max_position_embeddings=4096, layer_types=["full_attention"] * 2,
              max_window_layers=2)
    cfg_dict = {k: v for k, v in base.items() if k not in ("auto_map", "architectures", "language_config")}
    cfg = cfg_cls(language_config=lc, **cfg_dict)
    cfg.language_config._attn_implementation = "eager"
    torch.manual_seed(0)
    model = model_cls(cfg).float().eval()
    for name, p in model.named_parameters():
        if p.ndim == 2:
            p.normal_(0, 0.25, generator=g)
    # Make the text head pick im_end soon after the audio ends, so the run stops.
    model.lm_heads[0].weight[cfg.im_end_token_id] *= 1.5
    proc = processor(a.dev, False)
    refs = [torch.randint(0, 1024, (n, N_VQ), generator=g) for n in (12, 9)]
    prompt = torch.cat(refs, dim=0)
    text = "[S1]你好。[S2]Hi there.[S1]再见。"
    conv = [[proc.build_user_message(text=text, reference=refs), proc.build_assistant_message(audio_codes_list=[prompt])]]
    batch = proc(conv, mode="continuation")
    ids = batch["input_ids"]
    outs = model.generate(input_ids=ids, attention_mask=batch["attention_mask"], max_new_tokens=80,
                          text_temperature=0, audio_temperature=0, audio_repetition_penalty=1.1)
    start, gen = outs[0]
    gen = gen.numpy().astype(np.int32)
    os.makedirs(a.tiny_dir, exist_ok=True)
    from safetensors.numpy import save_file
    sd = {k: v.float().numpy() for k, v in model.state_dict().items()}
    save_file(sd, os.path.join(a.tiny_dir, "model.safetensors"))
    tiny_cfg = dict(base)
    tiny_cfg.pop("auto_map", None)
    tiny_cfg["language_config"] = lc
    json.dump(tiny_cfg, open(os.path.join(a.tiny_dir, "config.json"), "w"), indent=1)
    texts = gen[:, 0].tolist()
    json.dump({"inputIds": ids[0].numpy().astype(int).tolist(), "startLength": int(start),
               "generation": gen.astype(int).tolist(), "maxNewTokens": 80, "repetitionPenalty": 1.1},
              open(os.path.join(a.tiny_dir, "run.json"), "w"))
    print("tiny: prompt", ids.shape[1], "rows; assistant rows", len(texts), "(prompt part", int(start), "); text ids at the end:", texts[-24:])


# ---------------------------------------------------------------- ttsd

def recipe(dev):
    """The Space's text functions (clis/moss_ttsd_app.py), taken from its source."""
    path = os.path.join(dev, "src/moss-tts/clis/moss_ttsd_app.py")
    src = open(path).read()
    ns = {"re": re}
    for node in ast.parse(src).body:
        if isinstance(node, ast.FunctionDef) and node.name in (
                "normalize_text", "_merge_consecutive_speaker_tags", "_normalize_prompt_text", "_build_prefixed_text"):
            code = ast.get_source_segment(src, node).replace("dict[int, str]", "dict").replace("list[int]", "list")
            exec(code, ns)
    return ns


class Streamer:
    """The official model's forward (MossTTSDelayModel.forward with
    Qwen3Model), one decoder layer in memory at a time."""

    def __init__(self, dev, weights):
        from transformers import AutoConfig
        d = os.path.join(dev, "upstream/ttsd")
        self.cfg = AutoConfig.from_pretrained(d, trust_remote_code=True)
        self.lc = self.cfg.language_config
        self.lc._attn_implementation = "eager"
        self.get = weights  # name → float32 torch tensor

    def forward(self, ids, keep):
        from transformers.models.qwen3.modeling_qwen3 import Qwen3DecoderLayer, Qwen3RMSNorm, Qwen3RotaryEmbedding
        T = ids.shape[0]
        emb = self.get("language_model.embed_tokens.weight")
        h = emb[ids[:, 0]].clone()
        del emb
        for i in range(N_VQ):
            h += self.get(f"emb_ext.{i}.weight")[ids[:, i + 1]]
        h = h.unsqueeze(0)
        pos = torch.arange(T).unsqueeze(0)
        rot = Qwen3RotaryEmbedding(self.lc)
        cos_sin = rot(h, pos)
        mask = torch.full((T, T), float("-inf")).triu(1)[None, None]
        first = None
        for i in range(self.lc.num_hidden_layers):
            layer = Qwen3DecoderLayer(self.lc, i).float().eval()
            sd = {k: self.get(f"language_model.layers.{i}.{k}") for k in layer.state_dict()}
            layer.load_state_dict(sd)
            o = layer(h, attention_mask=mask, position_ids=pos, position_embeddings=cos_sin)
            h = o[0] if isinstance(o, tuple) else o
            if first is None:
                first = h[0, -1].clone()
            del layer, sd
        norm = Qwen3RMSNorm(self.lc.hidden_size, eps=self.lc.rms_norm_eps)
        norm.weight.copy_(self.get("language_model.norm.weight"))
        h = norm(h)[0, -keep:]
        heads = []
        for i in range(N_VQ + 1):
            w = self.get(f"lm_heads.{i}.weight")
            l = h @ w.T
            if i > 0:
                l[..., -1] = float("-inf")  # forward(): audio heads never predict their last row
            heads.append(l)
        return heads, first, h[-1]


def upstream_weights(d):
    from safetensors import safe_open
    index = json.load(open(os.path.join(d, "model.safetensors.index.json")))["weight_map"]
    handles = {}

    def get(name):
        f = index[name]
        if f not in handles:
            handles[f] = safe_open(os.path.join(d, f), framework="pt")
        return handles[f].get_tensor(name).float()
    return get


def converted_weights(d):
    import mlx.core as mx
    index = json.load(open(os.path.join(d, "model.safetensors.index.json")))["weight_map"]
    cache = {}

    def get(name):
        f = index[name]
        if f not in cache:
            cache.clear()
            cache[f] = mx.load(os.path.join(d, f))
        return torch.from_numpy(mlx_dequantize(cache[f], name))
    return get


def step_ttsd(a):
    from safetensors.numpy import load_file, save_file
    tok = load_file(a.tok_fixtures)
    ns = recipe(a.dev)
    texts = bytes(tok["refText"]).decode().split("\x00")
    refs = [torch.from_numpy(tok["codes0"]).long(), torch.from_numpy(tok["codes1"]).long()]
    prompt = torch.from_numpy(tok["codesCat"]).long()
    dialogue = "".join(f"[S{s}]{t}" for s, t in a.turn)
    d = ns["normalize_text"](dialogue)
    pm = {i + 1: ns["_normalize_prompt_text"](t, i + 1) for i, t in enumerate(texts)}
    full = ns["normalize_text"](ns["_build_prefixed_text"](d, pm, [1, 2]))
    proc = processor(a.dev, False)
    conv = [[proc.build_user_message(text=full, reference=refs), proc.build_assistant_message(audio_codes_list=[prompt])]]
    ids = proc(conv, mode="continuation")["input_ids"][0]
    # 24 teacher-forced audio rows: the second reference's delayed codes, as gen slots.
    delayed = proc.apply_delay_pattern(refs[1], 1024)[N_VQ: N_VQ + 24]
    ext = torch.cat([torch.full((24, 1), proc.model_config.audio_assistant_gen_slot_token_id), delayed], dim=1)
    ids_ext = torch.cat([ids, ext], dim=0)
    keep = 25
    out = {}
    if os.path.exists(a.out):
        out = dict(load_file(a.out))
    out.update({"inputIds": ids_ext.numpy().astype(np.int32), "promptRows": np.array([ids.shape[0]], np.int32),
                "text": np.frombuffer(full.encode(), dtype=np.uint8),
                "turns": np.frombuffer(json.dumps(a.turn, ensure_ascii=False).encode(), dtype=np.uint8)})
    print("prompt rows", ids.shape[0], "text", full)
    sources = {"official": ("", lambda: upstream_weights(os.path.join(a.dev, "upstream/ttsd"))),
               "q8": ("q8.", lambda: converted_weights(a.converted))}
    for which in a.which.split(","):
        prefix, get = sources[which]
        heads, first, last = Streamer(a.dev, get()).forward(ids_ext, keep)
        out[prefix + "textLogits"] = heads[0].numpy()
        out[prefix + "audioLogits"] = torch.stack(heads[1:], dim=1).numpy()  # (keep, 16, 1025)
        out[prefix + "layer0Last"] = first.numpy()
        out[prefix + "normLast"] = last.numpy()
        print(which, "done; text argmax at the end", int(heads[0][-1].argmax()))
    save_file({k: np.ascontiguousarray(v) for k, v in out.items()}, a.out)
    print("wrote", a.out)


def main():
    ap = argparse.ArgumentParser()
    sub = ap.add_subparsers(dest="step", required=True)
    t = sub.add_parser("tok")
    t.add_argument("dev"); t.add_argument("converted"); t.add_argument("out")
    t.add_argument("--ref", nargs=2, action="append", required=True)
    c = sub.add_parser("cases")
    c.add_argument("dev"); c.add_argument("sampler_json"); c.add_argument("tiny_dir")
    s = sub.add_parser("ttsd")
    s.add_argument("dev"); s.add_argument("converted"); s.add_argument("tok_fixtures"); s.add_argument("out")
    s.add_argument("--turn", nargs=2, action="append", required=True)
    s.add_argument("--which", default="official,q8", help="official, q8 or both (comma-separated)")
    a = ap.parse_args()
    {"tok": step_tok, "cases": step_cases, "ttsd": step_ttsd}[a.step](a)


if __name__ == "__main__":
    main()
