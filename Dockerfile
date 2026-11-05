FROM golang:1.26-alpine AS build
ENV GOTOOLCHAIN=auto
WORKDIR /src
COPY . .
RUN go mod tidy && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/flashcard .

FROM alpine:3
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /out/flashcard /app/flashcard
ENV DATA_DIR=/data
EXPOSE 3090
ENTRYPOINT ["/app/flashcard"]
