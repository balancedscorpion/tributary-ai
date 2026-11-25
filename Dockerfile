# syntax=docker/dockerfile:1.6
FROM --platform=$BUILDPLATFORM node:20-alpine AS ui
ARG SHOW_ALL_PROVIDERS=false
WORKDIR /app
ENV VITE_SHOW_ALL_PROVIDERS=$SHOW_ALL_PROVIDERS

COPY package.json package-lock.json ./
RUN --mount=type=cache,target=/root/.npm \
    npm ci --no-audit --no-fund

COPY . .
RUN npm run build


# Build Go binary on the build platform and cross-compile for target.
FROM --platform=$BUILDPLATFORM golang:1.22-alpine AS gobuild
WORKDIR /app
ARG SHOW_ALL_PROVIDERS=false
ARG TARGETOS
ARG TARGETARCH

COPY server/go.mod ./server/go.mod
COPY server/go.sum ./server/go.sum

COPY --from=ui /app/server/internal/webui/dist ./server/internal/webui/dist
COPY server/cmd ./server/cmd
COPY server/internal ./server/internal

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    cd server && \
    GOOS=$TARGETOS GOARCH=$TARGETARCH CGO_ENABLED=0 go build -trimpath -tags embedui -o /out/ai-analyst ./cmd/ai-analyst


FROM --platform=$TARGETPLATFORM debian:12-slim
WORKDIR /
ARG SHOW_ALL_PROVIDERS=false

ENV HOME=/home/nonroot
ENV AIA_SHOW_ALL_PROVIDERS=$SHOW_ALL_PROVIDERS

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
      ca-certificates \
      fd-find \
      poppler-utils \
      ripgrep \
      tesseract-ocr && \
    rm -rf /var/lib/apt/lists/*

COPY --from=gobuild /out/ai-analyst /ai-analyst

EXPOSE 8787
USER 0:0
ENTRYPOINT ["/ai-analyst", "--host", "0.0.0.0", "--port", "8787"]
