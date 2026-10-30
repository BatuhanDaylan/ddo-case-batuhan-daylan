# Aşama 1: derleme
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

# Aşama 2: küçük, root olmayan çalışma imajı
FROM alpine:3.20
RUN adduser -D -u 10001 app && apk add --no-cache wget
USER app
COPY --from=build /out/api /usr/local/bin/api
EXPOSE 8080
ENTRYPOINT ["api"]
