# ---- Build ----
FROM golang:1.24-alpine AS build
ENV GOTOOLCHAIN=auto
WORKDIR /src
COPY . .
RUN go get golang.org/x/crypto@v0.31.0 modernc.org/sqlite@v1.34.4 \
 && go mod tidy \
 && CGO_ENABLED=0 GOMAXPROCS=2 go build -p 1 -trimpath -ldflags="-s -w" -o /out/flashcard .

# ---- Run ----
FROM alpine:3
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /out/flashcard /app/flashcard
ENV DATA_DIR=/data
EXPOSE 3090
ENTRYPOINT ["/app/flashcard"]
