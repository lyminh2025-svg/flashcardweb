# Flashcard (Go + SQLite + Docker)

Trang web học bằng flashcard, chạy trên Raspberry Pi 4. Địa chỉ: **http://192.168.1.125:3090**
(Pi cần có IP tĩnh 192.168.1.125 trong router/DHCP.)

## Cài đặt
```bash
# 1. Cài Docker (nếu chưa có)
curl -fsSL https://get.docker.com | sh && sudo usermod -aG docker $USER   # đăng xuất/đăng nhập lại

# 2. Chép thư mục này lên Pi, rồi:
cd flashcard
cp .env.example .env && nano .env      # ĐỔI ADMIN_PASSWORD
docker compose up -d --build
docker compose logs -f                 # xem log
```
- Trang học: `http://192.168.1.125:3090`
- Quản trị: nút "Tạo nội dung" hoặc `http://192.168.1.125:3090/admin`

## Dữ liệu
Nằm trong `./data` (bền vững khi khởi động lại Docker):
- `data/flashcards.db` — SQLite (users, topics, flashcards)
- `data/uploads/` — hình ảnh (DB chỉ lưu đường dẫn)

## Sao lưu / phục hồi
- `./backup.sh` → tạo `backups/flashcard-*.tar.gz` (dừng app vài giây).
- Hoặc tải trực tiếp ở trang quản trị → tab "Sao lưu và tài khoản".
- `./restore.sh <file.tar.gz>` → phục hồi (dữ liệu cũ được giữ trong `data.cu-*`).

## Quên mật khẩu admin
Sửa `.env`: `ADMIN_PASSWORD=mat-khau-moi` và `ADMIN_RESET=true`, chạy `docker compose up -d`, sau đó đặt lại `ADMIN_RESET=false`.

## Cập nhật
`docker compose up -d --build`

## Ghi chú thiết kế
- Người học không cần đăng nhập. Yêu thích, khó nhớ, tiến độ, lịch sử và ôn tập ngắt quãng (Leitner: 10 phút, 1, 3, 7, 14, 30 ngày) được lưu **trên trình duyệt của từng thiết bị**. Khi cần theo dõi theo tài khoản người học, bảng `users` đã có cột `role` để mở rộng.
- Quyền admin kiểm tra ở backend (cookie phiên HttpOnly, mật khẩu băm bcrypt, giới hạn đăng nhập sai 5 lần/5 phút).
- Chỉ chạy trong mạng nội bộ qua HTTP. Nếu đưa ra Internet, hãy đặt sau reverse proxy HTTPS và đặt `SECURE_COOKIE=true`.
