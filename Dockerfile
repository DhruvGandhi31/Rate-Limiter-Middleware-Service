FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download || true
COPY . .
RUN CGO_ENABLED=0 go build -o /out/rate-limiter ./cmd/server

FROM alpine:3.20
RUN adduser -D -u 10001 app
COPY --from=build /out/rate-limiter /app/rate-limiter
WORKDIR /app
USER app
EXPOSE 8080
ENTRYPOINT ["/app/rate-limiter"]
