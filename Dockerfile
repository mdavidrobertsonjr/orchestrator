FROM node:22-alpine AS frontend-build
WORKDIR /src/frontend
COPY frontend/package*.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

FROM golang:1.25-alpine AS backend-build
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/api ./cmd/api
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/worker ./cmd/worker
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/hosted ./cmd/hosted

FROM alpine:3.22 AS api
WORKDIR /app/backend
RUN apk add --no-cache ca-certificates tzdata
COPY --from=backend-build /out/api /app/backend/api
COPY --from=frontend-build /src/frontend/dist /app/frontend/dist
ENV ORCH_ADDR=:8080
ENV ORCH_STATIC_DIR=/app/frontend/dist
EXPOSE 8080
CMD ["/app/backend/api"]

FROM alpine:3.22 AS worker
WORKDIR /app/backend
RUN apk add --no-cache ca-certificates tzdata
COPY --from=backend-build /out/worker /app/backend/worker
CMD ["/app/backend/worker"]

# Public website: visitors only need a browser. Codex runs on this server.
FROM node:22-bookworm-slim AS hosted
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && npm install --global @openai/codex@0.154.0
WORKDIR /app/backend
COPY --from=backend-build /out/hosted /app/backend/hosted
COPY --from=frontend-build /src/frontend/dist /app/frontend/dist
RUN mkdir -p /var/lib/orchestrator/users && chown -R node:node /var/lib/orchestrator
USER node
ENV ORCH_ADDR=:8080 ORCH_STATIC_DIR=/app/frontend/dist ORCH_USER_DATA_DIR=/var/lib/orchestrator/users
EXPOSE 8080
CMD ["/app/backend/hosted"]
