#!/usr/bin/env -S uv run
# /// script
# dependencies = ["pypdf>=6.1.1"]
# ///

from __future__ import annotations

import argparse
from pathlib import Path

from pypdf import PdfReader


def parse_pages(spec: str, total: int) -> list[int]:
    pages: list[int] = []
    for part in spec.split(","):
        part = part.strip()
        if not part:
            continue
        if "-" in part:
            start_s, end_s = part.split("-", 1)
            start = int(start_s)
            end = int(end_s)
            if start < 1 or end < start or end > total:
                raise ValueError(f"invalid page range {part!r}")
            pages.extend(range(start, end + 1))
            continue
        page = int(part)
        if page < 1 or page > total:
            raise ValueError(f"invalid page {part!r}")
        pages.append(page)
    return pages


def build_output(pdf: Path, pages_spec: str | None) -> str:
    reader = PdfReader(str(pdf))
    if pages_spec:
        pages = parse_pages(pages_spec, len(reader.pages))
    else:
        pages = list(range(1, len(reader.pages) + 1))

    parts = []
    for page_num in pages:
        text = reader.pages[page_num - 1].extract_text() or ""
        parts.append(f"===== PAGE {page_num} =====\n{text.rstrip()}\n")
    return "\n".join(parts)


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Extract plain text from sam_tut.pdf with page markers."
    )
    parser.add_argument(
        "pdf",
        nargs="?",
        default="../../sam_tut.pdf",
        help="path to the tutorial PDF (default: ../../sam_tut.pdf from ion/scripts)",
    )
    parser.add_argument(
        "--pages",
        help="comma-separated pages/ranges, e.g. 2-5,8,10",
    )
    parser.add_argument(
        "--output",
        help="write extracted text to this file instead of stdout",
    )
    args = parser.parse_args()

    script_dir = Path(__file__).resolve().parent
    pdf = (script_dir / args.pdf).resolve()
    output = build_output(pdf, args.pages)
    if args.output:
        out_path = Path(args.output).resolve()
        out_path.write_text(output, encoding="utf-8")
    else:
        print(output, end="")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
