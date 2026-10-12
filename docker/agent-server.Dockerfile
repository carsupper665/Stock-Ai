FROM golang:1.27.2-alpine3.23@sha256:9e45f0eb4a63ed37ad6e604950407558b7c738e34e015ae77a5d4ad369cde079 AS build
ARG BUILD_DATE
ARG REVISION
ARG SOURCE_DATE_EPOCH
ARG VERSION
WORKDIR /src/agent_server
ENV CGO_ENABLED=0 GOTOOLCHAIN=local
RUN test -n "$BUILD_DATE" && test -n "$REVISION" && test -n "$SOURCE_DATE_EPOCH" && test -n "$VERSION"
COPY agent_server/go.mod agent_server/go.sum ./
RUN go mod download
COPY agent_server/ ./
COPY docker/healthcheck.go /src/healthcheck.go
RUN go build -trimpath -buildvcs=false -tags=timetzdata -ldflags="-s -w -buildid=" -o /out/agent-server ./cmd/agent-server \
 && go build -trimpath -buildvcs=false -tags=timetzdata -ldflags="-s -w -buildid=" -o /out/healthcheck /src/healthcheck.go \
 && mkdir -p /out/data/history

FROM scratch
ARG BUILD_DATE
ARG REVISION
ARG VERSION
LABEL org.opencontainers.image.created=$BUILD_DATE \
      org.opencontainers.image.revision=$REVISION \
      org.opencontainers.image.source="https://github.com/carsupper665/Stock-Ai" \
      org.opencontainers.image.title="Stock AI Agent Server" \
      org.opencontainers.image.version=$VERSION
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build --chown=65532:65532 /out/data /data
COPY --from=build /out/agent-server /agent-server
COPY --from=build /out/healthcheck /healthcheck
ENV AGENT_SERVER_ADDR=0.0.0.0:8080 AGENT_SERVER_DB_PATH=/data/agent-server.db AGENT_SERVER_HISTORY_DIR=/data/history
EXPOSE 8080
USER 65532:65532
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/healthcheck", "http://127.0.0.1:8080/health"]
ENTRYPOINT ["/agent-server"]
