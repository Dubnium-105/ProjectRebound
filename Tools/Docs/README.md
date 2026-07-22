# Documentation checks

Run both documentation validators from any directory:

```bash
python Tools/Docs/check_markdown_links.py
python Tools/Docs/check_bilingual_docs.py
```

`check_markdown_links.py` checks relative links under the root English/Chinese READMEs, `docs/`, `Backend/`, `Desktop/`, and `Tools/`. External URLs and page fragments are intentionally not fetched.

`check_bilingual_docs.py` validates every English/Simplified Chinese pair registered in `docs/bilingual-docs.txt`. It requires the standard language switch near the H1 and matching heading and fenced-code structure, and rejects unregistered localized files or maintained documentation entry points.
