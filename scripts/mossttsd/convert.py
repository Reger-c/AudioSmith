"""Converts OpenMOSS-Team/MOSS-TTSD-v1.0 and its audio tokenizer for AudioSmith's engine (thread #82).

Developer tool, never shipped. Run it in the venv of scripts/mossttsd/setup-dev.sh:

    python scripts/mossttsd/convert.py tok  TOK_DIR  OUT_DIR [--swift-params codec-params.json]
    python scripts/mossttsd/convert.py ttsd TTSD_DIR OUT_DIR [--commit SHA]

The two steps can run apart (each upstream folder is deleted after its
step on the Mac); both write into the same OUT_DIR, one model folder:

  config.json                     upstream's, plus "quantization" (group 64,
                                  8 bits) and without auto_map
  model-0000N-of-0000M.safetensors, model.safetensors.index.json
                                  the language model (Qwen3-8B backbone,
                                  16 audio embeddings, 17 heads), upstream's
                                  names; every Linear and Embedding quantized
                                  to 8 bits (MLX affine, group 64, bfloat16
                                  scales and biases), norms bfloat16
  codec.safetensors, codec_config.json
                                  MOSS-Audio-Tokenizer: weight norm folded,
                                  1×1 convolutions as matrices, attention
                                  projections renamed (in_projs.0 → in_proj),
                                  only the 16 quantizers MOSS-TTSD reads; every
                                  Linear whose input is a multiple of 64
                                  quantized to 8 bits (float32 scales), the
                                  rest float32
  tokenizer files, generation_config.json, chat_template.jinja   upstream's
  LICENSE, NOTICE, README.md, conversion.json
"""
import argparse, json, os, re, shutil, subprocess, sys

import numpy as np

GROUP, BITS = 64, 8
N_VQ = 16  # MOSS-TTSD's codebooks; the tokenizer has 32
TTSD = "OpenMOSS-Team/MOSS-TTSD-v1.0@c7cd852d87aff71cab5bd2b9b05509cedc0ef1ba"
TOK = "OpenMOSS-Team/MOSS-Audio-Tokenizer@3cd226ba2947efa357ef453bcad111b6eafba782"


def quantize(mx, w, dtype):
    """(weight uint32, scales, biases) of w, MLX affine 8-bit, group 64."""
    wq, s, b = mx.quantize(mx.array(w).astype(dtype), group_size=GROUP, bits=BITS)
    return wq, s, b


# ---------------------------------------------------------------- tokenizer

def tok_key(k):
    """Upstream key → ours (None: dropped)."""
    m = re.match(r"^quantizer\.quantizers\.(\d+)\.", k)
    if m and int(m.group(1)) >= N_VQ:
        return None
    k = k.replace(".self_attn.in_projs.0.", ".self_attn.in_proj.").replace(".self_attn.out_projs.0.", ".self_attn.out_proj.")
    return k


def convert_tok(src, out, swift_params=None):
    import mlx.core as mx
    from safetensors import safe_open
    files = sorted(f for f in os.listdir(src) if f.endswith(".safetensors"))
    raw = {}
    for f in files:
        with safe_open(os.path.join(src, f), framework="numpy") as h:
            for k in h.keys():
                raw[k] = h.get_tensor(k)
    # Weight norm: weight = g · v / |v| (norm over all but the output axis).
    folded = {}
    for k, v in raw.items():
        if k.endswith(".parametrizations.weight.original0"):
            base = k[: -len(".parametrizations.weight.original0")]
            g = v.astype(np.float64)
            w = raw[base + ".parametrizations.weight.original1"].astype(np.float64)
            norm = np.sqrt((w ** 2).sum(axis=tuple(range(1, w.ndim)), keepdims=True))
            folded[base + ".weight"] = (g * w / norm).astype(np.float32)
        elif k.endswith(".parametrizations.weight.original1"):
            continue
        else:
            folded[k] = v.astype(np.float32)
    arrays = {}
    quantized = 0
    for k, v in folded.items():
        nk = tok_key(k)
        if nk is None:
            continue
        if v.ndim == 3 and v.shape[2] == 1:  # Conv1d kernel 1 → matrix (out, in)
            v = v[:, :, 0]
        linear = (nk.endswith(".weight") and v.ndim == 2 and ".codebook." not in nk
                  and not nk.startswith("quantizer."))
        if linear and v.shape[1] % GROUP == 0:
            wq, s, b = quantize(mx, v, mx.float32)
            arrays[nk] = wq
            arrays[nk[: -len("weight")] + "scales"] = s
            arrays[nk[: -len("weight")] + "biases"] = b
            quantized += 1
        else:
            arrays[nk] = mx.array(np.ascontiguousarray(v))
    if swift_params:
        check(arrays, swift_params, "codec")
    mx.save_safetensors(os.path.join(out, "codec.safetensors"), arrays, metadata={"format": "mlx"})
    cfg = json.load(open(os.path.join(src, "config.json")))
    cfg.pop("auto_map", None)
    cfg.pop("architectures", None)
    cfg["quantizer_kwargs"]["num_quantizers"] = N_VQ
    cfg["quantization"] = {"group_size": GROUP, "bits": BITS}
    json.dump(cfg, open(os.path.join(out, "codec_config.json"), "w"), indent=1)
    print(f"codec: {len(arrays)} tensors, {quantized} linears quantized")


# ---------------------------------------------------------------- language model

def convert_ttsd(src, out, swift_params=None, shard_bytes=4_500_000_000, delete_source=False):
    import mlx.core as mx
    index = json.load(open(os.path.join(src, "model.safetensors.index.json")))["weight_map"]
    files = sorted(set(index.values()))
    for f in os.listdir(out):
        if re.match(r"^model-\d+-of-\d+\.safetensors$", f):
            os.remove(os.path.join(out, f))
    shards, cur, size = [], {}, 0
    names = {}

    def flush():
        nonlocal cur, size
        if cur:
            shards.append(cur)
            tmp = os.path.join(out, f".shard-{len(shards)}.safetensors")
            mx.save_safetensors(tmp, cur, metadata={"format": "mlx"})
            for k in cur:
                names[k] = tmp
            cur, size = {}, 0

    count = 0
    for f in files:
        weights = mx.load(os.path.join(src, f))
        for k in sorted(weights):
            v = weights[k]
            quant = k.endswith(".weight") and v.ndim == 2  # Linear and Embedding
            if quant:
                wq, s, b = mx.quantize(v.astype(mx.bfloat16), group_size=GROUP, bits=BITS)
                mx.eval(wq, s, b)
                base = k[: -len("weight")]
                part = {k: wq, base + "scales": s, base + "biases": b}
                count += 1
            else:
                part = {k: v.astype(mx.bfloat16)}
                mx.eval(part[k])
            for pk, pv in part.items():
                cur[pk] = pv
                size += pv.nbytes
            if size >= shard_bytes:
                flush()
        del weights
        if delete_source:  # the Mac's disk: each upstream shard goes once converted
            os.remove(os.path.join(src, f))
    flush()
    n = len(shards)
    weight_map = {}
    for i in range(n):
        tmp = os.path.join(out, f".shard-{i + 1}.safetensors")
        name = f"model-{i + 1:05d}-of-{n:05d}.safetensors"
        os.rename(tmp, os.path.join(out, name))
        for k in shards[i]:
            weight_map[k] = name
    total = sum(v.nbytes for s in shards for v in s.values())
    json.dump({"metadata": {"total_size": total}, "weight_map": dict(sorted(weight_map.items()))},
              open(os.path.join(out, "model.safetensors.index.json"), "w"), indent=1)
    if swift_params:
        check({k: v for s in shards for k, v in s.items()}, swift_params, "language model")
    cfg = json.load(open(os.path.join(src, "config.json")))
    cfg.pop("auto_map", None)
    cfg["quantization"] = {"group_size": GROUP, "bits": BITS}
    json.dump(cfg, open(os.path.join(out, "config.json"), "w"), indent=1)
    for name in ("tokenizer.json", "tokenizer_config.json", "vocab.json", "merges.txt", "added_tokens.json",
                 "special_tokens_map.json", "generation_config.json", "chat_template.jinja"):
        shutil.copy(os.path.join(src, name), os.path.join(out, name))
    print(f"language model: {count} tensors quantized, {n} shards, {total / 1e9:.2f} GB")


def check(arrays, params_path, what):
    want = json.load(open(params_path))
    missing = [k for k in want if k not in arrays]
    extra = [k for k in arrays if k not in want]
    shape = [(k, list(arrays[k].shape), want[k]) for k in arrays if k in want and list(arrays[k].shape) != want[k]]
    for name, items in (("missing", missing), ("unexpected", extra), ("shape", shape)):
        for i in items[:20]:
            print(name, i)
    if missing or extra or shape:
        sys.exit(f"{what} keys do not match the Swift module: {len(missing)} missing, {len(extra)} unexpected, {len(shape)} shapes")
    print(f"{what}: {len(arrays)} tensors match the Swift module")


def finish(out, commit):
    here = os.path.dirname(os.path.abspath(__file__))
    for name in ("README.md", "NOTICE", "LICENSE"):
        s = os.path.join(here, "repo", name)
        if os.path.exists(s):
            shutil.copy(s, os.path.join(out, name))
    if not commit:
        try:
            commit = subprocess.run(["git", "-C", here, "rev-parse", "HEAD"], capture_output=True, text=True).stdout.strip()
        except OSError:
            commit = ""
    json.dump({"converter": "AudioSmith scripts/mossttsd/convert.py", "commit": commit or "unknown",
               "upstream": [TTSD, TOK], "quantization": {"group_size": GROUP, "bits": BITS}},
              open(os.path.join(out, "conversion.json"), "w"), indent=1)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("step", choices=["tok", "ttsd"])
    ap.add_argument("src")
    ap.add_argument("out")
    ap.add_argument("--swift-params")
    ap.add_argument("--commit", help="AudioSmith commit of this script (default: git rev-parse HEAD)")
    ap.add_argument("--delete-source", action="store_true", help="ttsd: delete each upstream shard once converted")
    a = ap.parse_args()
    os.makedirs(a.out, exist_ok=True)
    if a.step == "tok":
        convert_tok(a.src, a.out, a.swift_params)
    else:
        convert_ttsd(a.src, a.out, a.swift_params, delete_source=a.delete_source)
    finish(a.out, a.commit)
    print("done:", a.out)


if __name__ == "__main__":
    main()
