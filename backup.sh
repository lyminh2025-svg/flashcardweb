#!/usr/bin/env bash
# Sao lưu database + hình ảnh thành 1 file .tar.gz (tạm dừng app vài giây để dữ liệu nhất quán)
set -euo pipefail
cd "$(dirname "$0")"
mkdir -p backups
out="backups/flashcard-$(date +%Y%m%d-%H%M%S).tar.gz"
docker compose stop
tar -czf "$out" -C data flashcards.db uploads 2>/dev/null || tar -czf "$out" -C data .
docker compose start
echo "Đã sao lưu: $out"
