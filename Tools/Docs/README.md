# Documentation checks

Run the repository-local Markdown link validator from any directory:

```bash
python Tools/Docs/check_markdown_links.py
```

The script checks relative links under the root README, `docs/`, `Backend/`, `Desktop/` and `Tools/`. External URLs and page fragments are intentionally not fetched.
