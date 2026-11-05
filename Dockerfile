# ---- Build ----
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY . .
RUN go mod tidy && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/flashcard .

# ---- Run ----
FROM alpine:3
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /out/flashcard /app/flashcard
ENV PORT=3090 DATA_DIR=/data
VOLUME /data
EXPOSE 3090
ENTRYPOINT ["/app/flashcard"]
