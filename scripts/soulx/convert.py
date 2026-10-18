"""Converts Soul-AILab/SoulX-Podcast-1.7B for AudioSmith's engine (thread #80).

Developer tool, never shipped. Run it in the venv of scripts/soulx/setup-dev.sh:

    python scripts/soulx/convert.py UPSTREAM_DIR OUT_DIR [--swift-params params.json]

UPSTREAM_DIR is the downloaded upstream repo (LLM safetensors, flow.pt,
hift.pt, campplus.onnx, tokenizer). OUT_DIR gets one model folder:

  config.json, model*.safetensors   the Qwen3 language model, 8-bit (mlx_lm,
                                    group 64, bfloat16 scales); the tied
                                    lm_head is dropped
  tokenizer files, generation_config.json, soulxpodcast_config.json   as upstream
  s3gen.safetensors                 flow (encoder + CFM decoder), HiFT
                                    vocoder (mel2wav.*) and CAM++ speaker
                                    encoder (speaker_encoder.*), float32, keyed
                                    by mlx-audio-swift's CausalMaskedDiffWithXvec
  LICENSE, NOTICE, README.md        Apache-2.0 and the attributions

What the conversion does to the acoustic weights (flow.pt, hift.pt and
campplus.onnx are byte-identical to FunAudioLLM/CosyVoice2-0.5B's):
  - renames PyTorch modules to the Swift ones (Sequential indices → names);
  - folds weight norm (HiFT: weight = g · v / |v|, per output channel);
  - transposes convolutions to MLX's layout (Conv1d O,I,K → O,K,I;
    ConvTranspose1d I,O,K → O,K,I; Conv2d O,I,H,W → O,H,W,I);
  - CAM++ comes from the ONNX graph, where the exporter folded each batch
    norm that follows a convolution into it (anonymous onnx::Conv_* tensors):
    those convolutions keep the folded weight, and the batch norm after them
    becomes the identity plus the folded bias (weight 1, running mean 0,
    running var 1 − eps).
--swift-params (from `audiosmith-engine --soulx-dump-params`) checks every
key and shape against the Swift module tree.
"""
import argparse, json, os, re, shutil, subprocess, sys
import numpy as np

EPS = 1e-5  # MLXNN BatchNorm's eps


def flow_key(k):
    """flow.pt key → Swift key (None: dropped)."""
    k2 = k
    k2 = re.sub(r"^encoder\.(embed|up_embed)\.out\.0\.", r"encoder.\1.linear.", k2)
    k2 = re.sub(r"^encoder\.(embed|up_embed)\.out\.1\.", r"encoder.\1.norm.", k2)
    m = re.match(r"^decoder\.estimator\.(down|mid|up)_blocks\.(\d+)\.(\d+)\.(.*)$", k2)
    if m:
        kind, b, part, rest = m.groups()
        base = f"decoder.estimator.{kind}_blocks.{b}."
        if part == "0":  # ResnetBlock1D
            rest = re.sub(r"^(block[12])\.block\.0\.", r"\1.block.0.conv.conv.", rest)
            rest = re.sub(r"^(block[12])\.block\.2\.", r"\1.block.1.", rest)
            rest = re.sub(r"^mlp\.1\.", "mlp.0.", rest)
            rest = re.sub(r"^res_conv\.", "res_conv.conv.", rest)
            return base + "resnet." + rest
        if part == "1":  # transformer blocks
            t, rest = rest.split(".", 1)
            rest = rest.replace("ff.net.0.proj.", "ff.gelu_gate.proj.").replace("ff.net.2.", "ff.out_proj.")
            return base + f"transformer_blocks.{t}." + rest
        if part == "2":  # down/upsample (the last level: a causal conv)
            return base + ("downsample" if kind == "down" else "upsample") + ".conv.conv." + rest
        raise KeyError(k)
    k2 = re.sub(r"^decoder\.estimator\.final_block\.block\.0\.", "decoder.estimator.final_block.block.0.conv.conv.", k2)
    k2 = re.sub(r"^decoder\.estimator\.final_block\.block\.2\.", "decoder.estimator.final_block.block.1.", k2)
    k2 = re.sub(r"^decoder\.estimator\.final_proj\.", "decoder.estimator.final_proj.conv.", k2)
    return k2


def convert_flow(sd):
    out = {}
    for k, v in sd.items():
        v = v.float().numpy()
        nk = flow_key(k)
        if nk.endswith(".weight") and v.ndim == 3:
            v = v.transpose(0, 2, 1)  # Conv1d O,I,K → O,K,I
        out[nk] = v
    return out


def convert_hift(sd):
    sd = {k.replace("generator.", ""): v for k, v in sd.items()}
    folded = {}
    for k, v in sd.items():
        if ".parametrizations.weight.original0" in k:
            base = k.replace(".parametrizations.weight.original0", "")
            g = v.float().numpy()
            w = sd[base + ".parametrizations.weight.original1"].float().numpy()
            norm = np.sqrt((w.astype(np.float64) ** 2).sum(axis=tuple(range(1, w.ndim)), keepdims=True))
            folded[base + ".weight"] = (g * w / norm).astype(np.float32)
        elif ".parametrizations.weight.original1" in k:
            continue
        else:
            folded[k] = v.float().numpy()
    out = {}
    for k, v in folded.items():
        nk = k
        m = re.match(r"^f0_predictor\.condnet\.(\d+)\.(weight|bias)$", k)
        if m:
            nk = f"f0_predictor.condnet.{int(m.group(1)) // 2}.conv.{m.group(2)}"
        nk = re.sub(r"^(conv_pre|conv_post)\.(weight|bias)$", r"\1.conv.\2", nk)
        nk = re.sub(r"^(ups|source_downs)\.(\d+)\.(weight|bias)$", r"\1.\2.conv.\3", nk)
        nk = re.sub(r"^((?:source_)?resblocks\.\d+\.convs[12]\.\d+)\.(weight|bias)$", r"\1.conv.\2", nk)
        if nk.endswith(".weight") and v.ndim == 3:
            if re.match(r"^ups\.", k):
                v = v.transpose(1, 2, 0)  # ConvTranspose1d I,O,K → O,K,I
            else:
                v = v.transpose(0, 2, 1)
        out["mel2wav." + nk] = v
    return out


def convert_campplus(path):
    import onnx
    from onnx import numpy_helper
    m = onnx.load(path)
    inits = {i.name: numpy_helper.to_array(i).astype(np.float32) for i in m.graph.initializer}
    anon = [n for n in inits if n.startswith("onnx::Conv_")]
    anon.sort(key=lambda n: int(n.split("_")[-1]))
    weights = [n for n in anon if inits[n].ndim > 1]
    biases = {n: inits[n] for n in anon if inits[n].ndim == 1}
    folded = []  # (weight, bias) in graph order
    for w in weights:
        b = "onnx::Conv_%d" % (int(w.split("_")[-1]) + 1)
        folded.append((inits[w], biases[b]))
    out = {}

    def conv(key, w):
        if w.ndim == 4:
            out[key] = w.transpose(0, 2, 3, 1)
        else:
            out[key] = w.transpose(0, 2, 1)

    def identity_bn(key, bias, n):
        out[key + ".weight"] = np.ones(n, np.float32)
        out[key + ".bias"] = bias.astype(np.float32) if bias is not None else np.zeros(n, np.float32)
        out[key + ".running_mean"] = np.zeros(n, np.float32)
        out[key + ".running_var"] = np.full(n, 1 - EPS, np.float32)

    def named_bn(key, src, affine=True):
        n = inits[src + ".running_mean"].shape[0]
        out[key + ".weight"] = inits[src + ".weight"] if affine else np.ones(n, np.float32)
        out[key + ".bias"] = inits[src + ".bias"] if affine else np.zeros(n, np.float32)
        out[key + ".running_mean"] = inits[src + ".running_mean"]
        out[key + ".running_var"] = inits[src + ".running_var"]

    it = iter(folded)

    def take(key, bn):
        w, b = next(it)
        conv(key + ".weight", w)
        identity_bn(bn, b, w.shape[0])

    # FCM head: conv1/bn1; layer1, layer2 (two blocks each; the first with a shortcut); conv2/bn2.
    take("head.conv1", "head.bn1")
    for layer in ("layer1", "layer2"):
        take(f"head.{layer}.0.conv1", f"head.{layer}.0.bn1")
        take(f"head.{layer}.0.conv2", f"head.{layer}.0.bn2")
        take(f"head.{layer}.0.shortcut.0", f"head.{layer}.0.shortcut.1")
        take(f"head.{layer}.1.conv1", f"head.{layer}.1.bn1")
        take(f"head.{layer}.1.conv2", f"head.{layer}.1.bn2")
    take("head.conv2", "head.bn2")
    take("tdnn.linear", "tdnn.nonlinear.0")
    for b, layers in enumerate((12, 24, 16)):
        for l in range(layers):
            src = f"xvector.block{b + 1}.tdnnd{l + 1}"
            dst = f"blocks.{b}.layers.{l}"
            named_bn(dst + ".nonlinear1.0", src + ".nonlinear1.batchnorm")
            take(dst + ".linear1", dst + ".nonlinear2.0")
            conv(dst + ".cam_layer.linear_local.weight", inits[src + ".cam_layer.linear_local.weight"])
            for lin in ("linear1", "linear2"):
                conv(f"{dst}.cam_layer.{lin}.weight", inits[f"{src}.cam_layer.{lin}.weight"])
                out[f"{dst}.cam_layer.{lin}.bias"] = inits[f"{src}.cam_layer.{lin}.bias"]
            # mlx-audio-swift's CAMLayer has two batch norms upstream's lacks: identity.
            identity_bn(dst + ".cam_layer.bn1", None, inits[src + ".cam_layer.linear1.weight"].shape[0])
            identity_bn(dst + ".cam_layer.bn2", None, inits[src + ".cam_layer.linear2.weight"].shape[0])
    for t in range(3):
        src, dst = f"xvector.transit{t + 1}", f"transits.{t}"
        named_bn(dst + ".nonlinear.0", src + ".nonlinear.batchnorm")
        if t < 2:
            conv(dst + ".linear.weight", inits[src + ".linear.weight"])
            out[dst + ".linear.bias"] = np.zeros(inits[src + ".linear.weight"].shape[0], np.float32)
        else:  # the last transit carries out_nonlinear's batch norm
            w, b = next(it)
            conv(dst + ".linear.weight", w)
            out[dst + ".linear.bias"] = b
            identity_bn("out_nonlinear.0", None, w.shape[0])
    conv("dense.linear.weight", inits["xvector.dense.linear.weight"])
    named_bn("dense.nonlinear.0", "xvector.dense.nonlinear.batchnorm", affine=False)
    rest = list(it)
    assert not rest, f"{len(rest)} folded convolutions left over"
    return {"speaker_encoder." + k: v for k, v in out.items()}


def check(arrays, params_path):
    want = json.load(open(params_path))
    # Computed at init (positional encodings, the fixed noise table, the STFT
    # window) or unused here (Turbo's time mixer; CAMLayer's extra norms are
    # written as identities anyway).
    optional = re.compile(r"(\.pe$|stftWindow$|randNoise$|time_embed_mixer|^speaker_encoder\..*\.(bn1|bn2)\.)")
    missing = [k for k in want if k not in arrays and not optional.search(k)]
    extra = [k for k in arrays if k not in want]
    shape = [(k, list(arrays[k].shape), want[k]) for k in arrays if k in want and list(arrays[k].shape) != want[k]]
    for name, items in (("missing", missing), ("unexpected", extra), ("shape", shape)):
        for i in items[:20]:
            print(name, i)
    if missing or extra or shape:
        sys.exit(f"s3gen keys do not match the Swift module: {len(missing)} missing, {len(extra)} unexpected, {len(shape)} shapes")
    print(f"s3gen: {len(arrays)} tensors match the Swift module")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("upstream")
    ap.add_argument("out")
    ap.add_argument("--swift-params")
    ap.add_argument("--skip-llm", action="store_true")
    a = ap.parse_args()
    os.makedirs(a.out, exist_ok=True)

    import torch
    from safetensors.numpy import save_file
    arrays = {}
    arrays.update(convert_flow(torch.load(os.path.join(a.upstream, "flow.pt"), map_location="cpu", weights_only=True)))
    arrays.update(convert_hift(torch.load(os.path.join(a.upstream, "hift.pt"), map_location="cpu", weights_only=True)))
    arrays.update(convert_campplus(os.path.join(a.upstream, "campplus.onnx")))
    arrays = {k: np.ascontiguousarray(v, dtype=np.float32) for k, v in arrays.items()}
    if a.swift_params:
        check(arrays, a.swift_params)
    save_file(arrays, os.path.join(a.out, "s3gen.safetensors"), metadata={"format": "audiosmith-s3gen/1"})
    print("wrote s3gen.safetensors:", len(arrays), "tensors")

    if not a.skip_llm:
        from mlx_lm import convert
        tmp = os.path.join(a.out, ".llm-tmp")
        shutil.rmtree(tmp, ignore_errors=True)
        convert(a.upstream, mlx_path=tmp, quantize=True, q_group_size=64, q_bits=8, dtype="bfloat16")
        for name in os.listdir(tmp):
            if name.endswith(".safetensors") or name in ("config.json", "model.safetensors.index.json"):
                shutil.move(os.path.join(tmp, name), os.path.join(a.out, name))
        shutil.rmtree(tmp)
    for name in ("tokenizer.json", "tokenizer_config.json", "vocab.json", "merges.txt", "added_tokens.json",
                 "special_tokens_map.json", "generation_config.json", "soulxpodcast_config.json"):
        shutil.copy(os.path.join(a.upstream, name), os.path.join(a.out, name))
    here = os.path.dirname(os.path.abspath(__file__))
    for name in ("README.md", "NOTICE", "LICENSE"):
        src = os.path.join(here, "repo", name)
        if os.path.exists(src):
            shutil.copy(src, os.path.join(a.out, name))
    try:
        commit = subprocess.run(["git", "-C", here, "rev-parse", "HEAD"], capture_output=True, text=True).stdout.strip()
    except OSError:
        commit = ""
    json.dump({"converter": "AudioSmith scripts/soulx/convert.py", "commit": commit or "unknown",
               "upstream": "Soul-AILab/SoulX-Podcast-1.7B@7d381e3aa4f2ac9e23201b57957fd0499f8f2fd1"},
              open(os.path.join(a.out, "conversion.json"), "w"), indent=1)
    print("done:", a.out)


if __name__ == "__main__":
    main()
