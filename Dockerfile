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
