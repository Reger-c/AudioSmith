"""Uploads the converted SoulX-Podcast folder to the Hugging Face repo (thread #80).

Developer tool, never shipped; run it in the venv of setup-dev.sh after the
parity checks pass (parity.sh check, check32):

    python scripts/soulx/upload.py CONVERTED_DIR [--repo actnowai/SoulX-Podcast-1.7B-MLX-8bit]

The token is read from ~/.cache/huggingface/token (never printed). Prints
the commit to pin in internal/catalog/catalog.json, with the files' sizes
and sha256 for the entry.
"""
import argparse, hashlib, json, os
from huggingface_hub import HfApi

FILES = ["config.json", "model.safetensors", "model.safetensors.index.json", "s3gen.safetensors", "tokenizer.json",
         "tokenizer_config.json", "vocab.json", "merges.txt", "added_tokens.json", "special_tokens_map.json",
         "generation_config.json", "soulxpodcast_config.json", "conversion.json", "README.md", "NOTICE", "LICENSE"]


def sha256(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for b in iter(lambda: f.read(1 << 20), b""):
            h.update(b)
    return h.hexdigest()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("folder")
    ap.add_argument("--repo", default="actnowai/SoulX-Podcast-1.7B-MLX-8bit")
    a = ap.parse_args()
    missing = [f for f in FILES if not os.path.exists(os.path.join(a.folder, f))]
    if missing:
        raise SystemExit(f"missing: {missing}")
    token = open(os.path.expanduser("~/.cache/huggingface/token")).read().strip()
    api = HfApi(token=token)
    api.create_repo(a.repo, repo_type="model", exist_ok=True, private=False)
    info = api.upload_folder(repo_id=a.repo, folder_path=a.folder, allow_patterns=FILES,
                             commit_message="SoulX-Podcast-1.7B for MLX: 8-bit language model, s3gen.safetensors (AudioSmith convert.py)")
    sha = api.repo_info(a.repo).sha
    print("commit", sha, info)
    entry = [{"path": f, "size": os.path.getsize(os.path.join(a.folder, f)),
              **({"sha256": sha256(os.path.join(a.folder, f))} if f.endswith(".safetensors") or f == "tokenizer.json" else {})}
             for f in FILES]
    print(json.dumps({"revision": sha, "files": entry, "size": sum(e["size"] for e in entry)}, indent=1))


if __name__ == "__main__":
    main()
