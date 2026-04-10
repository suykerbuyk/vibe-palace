#!/usr/bin/env python3
"""Export MemPalace data (ChromaDB + SQLite KG) to JSON for vibe-palace import.

Usage:
    python3 scripts/export_mempalace.py \
        --palace-path ~/.mempalace/palace \
        --kg-path ~/.mempalace/knowledge_graph.sqlite3 \
        --output mempalace-export.json

Requirements:
    pip install chromadb
"""

import argparse
import json
import os
import sqlite3
import sys
from datetime import datetime, timezone


def _row_get(row: sqlite3.Row, column: str, default=None):
    """Safe column access for sqlite3.Row (which lacks .get())."""
    try:
        return row[column]
    except (IndexError, KeyError):
        return default


def export_chromadb(palace_path: str) -> list[dict]:
    """Export all drawers from ChromaDB collections."""
    try:
        import chromadb
    except ImportError:
        print("ERROR: chromadb not installed. Run: pip install chromadb", file=sys.stderr)
        sys.exit(1)

    if not os.path.isdir(palace_path):
        print(f"ERROR: palace path not found: {palace_path}", file=sys.stderr)
        sys.exit(1)

    client = chromadb.PersistentClient(path=palace_path)
    collections = client.list_collections()
    drawers = []

    for col in collections:
        # Each collection is a "wing" in MemPalace
        wing = col.name
        results = col.get(include=["documents", "metadatas"])

        for i, doc_id in enumerate(results["ids"]):
            meta = results["metadatas"][i] if results["metadatas"] else {}
            content = results["documents"][i] if results["documents"] else ""

            drawers.append({
                "id": doc_id,
                "wing": wing,
                "room": meta.get("room", ""),
                "content": content,
                "source_file": meta.get("source_file", ""),
                "chunk_index": meta.get("chunk_index", 0),
                "added_by": "mempalace",
                "filed_at": meta.get("filed_at", ""),
            })

    return drawers


def export_kg(kg_path: str) -> tuple[list[dict], list[dict]]:
    """Export entities and triples from SQLite knowledge graph."""
    if not os.path.isfile(kg_path):
        print(f"WARNING: KG database not found: {kg_path}", file=sys.stderr)
        return [], []

    conn = sqlite3.connect(kg_path)
    conn.row_factory = sqlite3.Row

    entities = []
    try:
        cursor = conn.execute("SELECT * FROM entities")
        for row in cursor:
            entities.append({
                "id": row["id"],
                "name": row["name"],
                "type": row["type"],
                "properties": json.loads(row["properties"]) if row["properties"] else {},
                "created_at": row["created_at"],
            })
    except sqlite3.OperationalError as e:
        print(f"WARNING: Could not read entities: {e}", file=sys.stderr)

    triples = []
    try:
        cursor = conn.execute("SELECT * FROM triples")
        for row in cursor:
            triples.append({
                "subject": row["subject"],
                "predicate": row["predicate"],
                "object": row["object"],
                "valid_from": _row_get(row, "valid_from", ""),
                "valid_to": _row_get(row, "valid_to", None),
                "confidence": _row_get(row, "confidence", 1.0),
                "source_session": _row_get(row, "source_session", ""),
            })
    except sqlite3.OperationalError as e:
        print(f"WARNING: Could not read triples: {e}", file=sys.stderr)

    conn.close()
    return entities, triples


def main():
    parser = argparse.ArgumentParser(
        description="Export MemPalace data to JSON for vibe-palace import"
    )
    parser.add_argument(
        "--palace-path",
        required=True,
        help="Path to MemPalace ChromaDB directory (e.g., ~/.mempalace/palace)",
    )
    parser.add_argument(
        "--kg-path",
        required=True,
        help="Path to MemPalace SQLite KG (e.g., ~/.mempalace/knowledge_graph.sqlite3)",
    )
    parser.add_argument(
        "--output",
        required=True,
        help="Output JSON file path",
    )

    args = parser.parse_args()

    palace_path = os.path.expanduser(args.palace_path)
    kg_path = os.path.expanduser(args.kg_path)

    print(f"Exporting ChromaDB from {palace_path}...", file=sys.stderr)
    drawers = export_chromadb(palace_path)
    print(f"  {len(drawers)} drawers exported", file=sys.stderr)

    print(f"Exporting KG from {kg_path}...", file=sys.stderr)
    entities, triples = export_kg(kg_path)
    print(f"  {len(entities)} entities, {len(triples)} triples exported", file=sys.stderr)

    export = {
        "exported_at": datetime.now(timezone.utc).isoformat(),
        "drawers": drawers,
        "entities": entities,
        "triples": triples,
    }

    output_path = os.path.expanduser(args.output)
    with open(output_path, "w") as f:
        json.dump(export, f, indent=2)

    print(f"\nExported to {output_path}:", file=sys.stderr)
    print(f"  {len(drawers)} drawers", file=sys.stderr)
    print(f"  {len(entities)} entities", file=sys.stderr)
    print(f"  {len(triples)} triples", file=sys.stderr)


if __name__ == "__main__":
    main()
