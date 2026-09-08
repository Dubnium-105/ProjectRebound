# Documentation checks

English | [简体中文](README.zh-CN.md)

Run both documentation validators from any directory:

```bash
python Tools/Docs/check_markdown_links.py
python Tools/Docs/check_bilingual_docs.py
```

`check_markdown_links.py` checks relative links under the root English/Chinese READMEs, `docs/`, `Backend/`, `Desktop/`, and `Tools/`. External URLs and page fragments are intentionally not fetched.

`check_bilingual_docs.py` automatically validates every maintained Markdown file in the repository. It requires a `.zh-CN.md` sibling, standard language switches, matching heading/fenced-code structure, and language-preserving internal links. Historical files under `docs/archive/` and generated dependency/build-cache directories are excluded. The dated acceptance snapshots under `docs/implementation/strict-roster-20260907/` and `docs/testing/hardware-test-20260908/` are also excluded: they are immutable evidence and contract snapshots, not maintained product manuals, so their original bytes and recorded language coverage remain unchanged.
