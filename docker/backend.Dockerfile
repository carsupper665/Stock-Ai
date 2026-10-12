FROM golang:1.27.2-alpine3.23@sha256:9e45f0eb4a63ed37ad6e604950407558b7c738e34e015ae77a5d4ad369cde079 AS build
ARG BUILD_DATE
ARG REVISION
ARG SOURCE_DATE_EPOCH
ARG VERSION
WORKDIR /src/backend
ENV CGO_ENABLED=0 GOTOOLCHAIN=local
RUN test -n "$BUILD_DATE" && test -n "$REVISION" && test -n "$SOURCE_DATE_EPOCH" && test -n "$VERSION"
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
COPY docker/healthcheck.go /src/healthcheck.go
RUN go build -trimpath -buildvcs=false -tags=timetzdata -ldflags="-s -w -buildid=" -o /out/backend . \
 && go build -trimpath -buildvcs=false -tags=timetzdata -ldflags="-s -w -buildid=" -o /out/healthcheck /src/healthcheck.go \
 && mkdir -p /out/data/logs

FROM scratch
ARG BUILD_DATE
ARG REVISION
ARG VERSION
LABEL org.opencontainers.image.created=$BUILD_DATE \
      org.opencontainers.image.revision=$REVISION \
      org.opencontainers.image.source="https://github.com/carsupper665/Stock-Ai" \
      org.opencontainers.image.title="Stock AI Backend" \
      org.opencontainers.image.version=$VERSION
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build --chown=65532:65532 /out/data /data
COPY --from=build /out/backend /backend
COPY --from=build /out/healthcheck /healthcheck
ENV BACKEND_ADDR=:7794 DB_DRIVER=sqlite DB_DSN=/data/backend.db LOG_DIR=/data/logs
EXPOSE 7794
USER 65532:65532
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/healthcheck", "http://127.0.0.1:7794/v1/health"]
ENTRYPOINT ["/backend"]
