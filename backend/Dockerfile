# Backend-only image (API + WebSocket gateway), for split deployments where
# the frontend is hosted separately (e.g. on Vercel).
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

FROM alpine:3.20
RUN apk add --no-cache ffmpeg ca-certificates tini && adduser -D -H app
COPY --from=build /out/server /app/server
USER app
ENV PORT=8080
EXPOSE 8080
ENTRYPOINT ["/sbin/tini", "--", "/app/server"]
