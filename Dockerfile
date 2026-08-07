FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/alice ./cmd/alice \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/alice-component-host ./cmd/alice-component-host

FROM golang:1.25-alpine
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=build /out/alice /app/alice
COPY --from=build /out/alice-component-host /app/alice-component-host
COPY go.mod go.sum /app/
COPY pkg /app/pkg
COPY components /app/components
RUN mkdir -p /app/data /app/.cache && addgroup -S alice && adduser -S -G alice alice && chown -R alice:alice /app
USER alice
EXPOSE 8080 8090
CMD ["/app/alice", "-addr", ":8080", "-data", "/app/data", "-components", "/app/components"]
