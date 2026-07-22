# Documentation and localization standard

English | [简体中文](documentation-standard.zh-CN.md)

ProjectRebound maintains public documentation in English and Simplified Chinese. English is the default file on GitHub; the Simplified Chinese sibling uses the BCP 47 suffix `.zh-CN.md`.

This layout follows two proven open-source conventions: Kubernetes uses language codes and separate localization content, while Ant Design exposes a direct `English / 中文` switch near the top of its README. See the [Kubernetes localization guide](https://kubernetes.io/docs/contribute/localization/) and the [Ant Design repository](https://github.com/ant-design/ant-design).

## File naming

| English default | Simplified Chinese |
| --- | --- |
| `README.md` | `README.zh-CN.md` |
| `overview.md` | `overview.zh-CN.md` |
| `relay-continuity.md` | `relay-continuity.zh-CN.md` |

Do not use `_cn`, `-cn`, `.zh`, or mixed-language suffixes. Locale-independent machine contracts such as OpenAPI, protobuf, SQL migrations, JSON, and YAML remain single-source files.

## Language switch

Every paired document places its switch immediately below the H1:

```markdown
English | [简体中文](document.zh-CN.md)
```

```markdown
[English](document.md) | 简体中文
```

Language-preserving links are required: an English index links to English documents, while a Chinese index links to `.zh-CN.md` documents when a translation exists.

## Content parity

- Both versions must describe the same supported behavior, safety boundary, commands, API version, and release state.
- Code blocks, endpoint paths, configuration keys, identifiers, image tags, units, and acceptance thresholds must remain identical.
- A normative change updates both languages in the same pull request. Translation-only wording improvements may be separate.
- English is the human-readable source for conflict resolution. Machine-readable contracts and implementation tests take precedence over both languages.
- Do not translate secrets, host-specific credentials, incident tokens, or historical evidence values.

## Scope and exceptions

All current public entry points and cross-component normative guides are registered in [`bilingual-docs.txt`](bilingual-docs.txt) and checked by CI. New public documents must be added to that registry with both files in the same commit.

Historical snapshots under `docs/archive/` are frozen and are not translated. Version evidence under `docs/testing/` may preserve the language in which the evidence was recorded, but its index and release conclusion must be bilingual. Implementation-adjacent notes may remain single-language until materially revised; once revised, they should follow this standard or link to a registered bilingual guide.

## Review checklist

1. Update the English and Chinese files together.
2. Verify language switches and language-preserving links.
3. Compare commands, paths, versions, numbers, tables, warnings, and release status.
4. Run `python Tools/Docs/check_markdown_links.py` and `python Tools/Docs/check_bilingual_docs.py`.
5. Treat a missing or stale translation as a release-documentation defect.
