FROM node:20-alpine AS css
WORKDIR /src
COPY package.json package-lock.json ./
RUN npm ci
COPY tailwind.config.js ./
COPY static ./static
RUN npm run build:css

FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=css /src/static/style.css /src/static/style.css
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/kanly-admin .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates docker-cli
WORKDIR /app
COPY --from=builder /out/kanly-admin /app/kanly-admin
RUN mkdir -p /app/data
ENV PORT=60000
ENV KANLY_DB_PATH=/app/data/kanly.db
EXPOSE 60000
ENTRYPOINT ["/app/kanly-admin"]
