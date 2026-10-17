FROM golang:1.27.2-alpine3.23@sha256:9e45f0eb4a63ed37ad6e604950407558b7c738e34e015ae77a5d4ad369cde079 AS build
ARG BUILD_DATE
ARG REVISION
ARG SOURCE_DATE_EPOCH
ARG VERSION
WORKDIR /src/llm_provider_server
ENV CGO_ENABLED=0 GOTOOLCHAIN=local
RUN test -n "$BUILD_DATE" && test -n "$REVISION" && test -n "$SOURCE_DATE_EPOCH" && test -n "$VERSION"
# Keep the complete native package: Codex locates the tool host and resources beside its entrypoint.
RUN wget -q -O /tmp/codex.tgz https://registry.npmjs.org/@openai/codex/-/codex-0.162.1-linux-x64.tgz \
 && echo "7232b699d1c87c47a9675950d4c5f7693402fbc9dc22cbe769c97feaf23674821c7cff3f32b853436faa46591714d0f32528f5777eaab88b333fedffe0250959  /tmp/codex.tgz" | sha512sum -c - \
 && mkdir -p /out/codex \
 && tar -xzf /tmp/codex.tgz -C /out/codex --strip-components=3 \
 && test -x /out/codex/bin/codex-code-mode-host \
 && test -f /out/codex/codex-package.json \
 && test "$(/out/codex/bin/codex --version)" = "codex-cli 0.162.1" \
 && /out/codex/bin/codex-code-mode-host --help >/dev/null \
 && rm /tmp/codex.tgz
COPY llm_provider_server/go.mod llm_provider_server/go.sum ./
RUN go mod download
COPY llm_provider_server/ ./
COPY docker/healthcheck.go /src/healthcheck.go
RUN go build -trimpath -buildvcs=false -tags=timetzdata -ldflags="-s -w -buildid=" -o /out/llm-provider-server ./cmd/llm-provider-server \
 && go build -trimpath -buildvcs=false -tags=timetzdata -ldflags="-s -w -buildid=" -o /out/healthcheck /src/healthcheck.go \
 && mkdir -p /out/data

FROM scratch
ARG BUILD_DATE
ARG REVISION
ARG VERSION
LABEL org.opencontainers.image.created=$BUILD_DATE \
      org.opencontainers.image.revision=$REVISION \
      org.opencontainers.image.source="https://github.com/carsupper665/Stock-Ai" \
      org.opencontainers.image.title="Stock AI LLM Provider Server" \
      org.opencontainers.image.version=$VERSION
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build --chown=65532:65532 /out/data /data
COPY --from=build /out/codex /opt/codex
COPY --from=build /out/llm-provider-server /llm-provider-server
COPY --from=build /out/healthcheck /healthcheck
ENV LLM_SERVER_CODEX_EXECUTABLE=/opt/codex/bin/codex
ENV LLM_SERVER_ADDR=0.0.0.0:8090 LLM_SERVER_DB_PATH=/data/llm-provider.db
EXPOSE 8090
USER 65532:65532
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/healthcheck", "http://127.0.0.1:8090/healthz"]
ENTRYPOINT ["/llm-provider-server"]
