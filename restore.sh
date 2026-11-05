#!/usr/bin/env bash
# Phục hồi từ file sao lưu: ./restore.sh backups/flashcard-XXXX.tar.gz
# (cũng dùng được với file tải từ nút "Tải bản sao lưu" trong trang quản trị)
set -euo pipefail
cd "$(dirname "$0")"
[ $# -eq 1 ] || { echo "Cách dùng: ./restore.sh <file.tar.gz>"; exit 1; }
[ -f "$1" ] || { echo "Không thấy file: $1"; exit 1; }
tmp="$(mktemp -d)"
tar -xzf "$1" -C "$tmp"
[ -f "$tmp/flashcards.db" ] || { echo "File sao lưu không hợp lệ (thiếu flashcards.db)"; rm -rf "$tmp"; exit 1; }
docker compose down
if [ -d data ] && [ -n "$(ls -A data 2>/dev/null)" ]; then
  mv data "data.cu-$(date +%Y%m%d-%H%M%S)"
fi
mkdir -p data/uploads
cp "$tmp/flashcards.db" data/flashcards.db
[ -d "$tmp/uploads" ] && cp -a "$tmp/uploads/." data/uploads/
rm -rf "$tmp"
docker compose up -d
echo "Đã phục hồi xong. Dữ liệu cũ (nếu có) được giữ trong thư mục data.cu-*"
