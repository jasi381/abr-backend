# --- build ---
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/abr-server ./cmd/server

# --- run: just the binary + ffmpeg ---
FROM alpine:3.20
RUN apk add --no-cache ffmpeg ca-certificates
COPY --from=build /out/abr-server /usr/local/bin/abr-server
ENV DATA_DIR=/data ADDR=:8080
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["abr-server"]
