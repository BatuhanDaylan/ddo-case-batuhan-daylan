# Aşama 1: derleme (api, simulator, verify tek seferde)
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/...

# Aşama 2: küçük, root olmayan çalışma imajı
FROM alpine:3.20
RUN adduser -D -u 10001 app && apk add --no-cache wget \
    && mkdir /data && chmod 777 /data
USER app
COPY --from=build /out/ /usr/local/bin/
EXPOSE 8080
ENTRYPOINT ["api"]
