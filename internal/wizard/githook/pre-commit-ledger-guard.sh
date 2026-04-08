#!/usr/bin/env bash
# Stoke ledger append-only guard. Installed by `stoke init`.
# Rejects commits that modify, delete, or rename files under .stoke/ledger/,
# with the exception of .stoke/ledger/.index.db (regenerable SQLite index).
set -euo pipefail
changed=$(git diff --cached --name-status)
violations=""
while IFS=$'\t' read -r status file rest; do
    [ -z "$status" ] && continue
    case "$file" in
        .stoke/ledger/.index.db*) continue ;;
        .stoke/ledger/*) ;;
        *) continue ;;
    esac
    case "$status" in
        A) continue ;;
        M|D|R*)
            violations="$violations\n  $status $file"
            ;;
    esac
done <<< "$changed"
if [ -n "$violations" ]; then
    echo "STOKE LEDGER GUARD: commit rejected" >&2
    printf "Disallowed operations on .stoke/ledger/ files:\n" >&2
    printf "$violations\n" >&2
    echo "The Stoke ledger is append-only. Create new nodes with supersedes edges instead." >&2
    exit 1
fi
exit 0
