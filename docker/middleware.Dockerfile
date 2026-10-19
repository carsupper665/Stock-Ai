FROM golang:1.27.2-alpine3.23@sha256:9e45f0eb4a63ed37ad6e604950407558b7c738e34e015ae77a5d4ad369cde079 AS build
WORKDIR /src/middleware
ENV CGO_ENABLED=0 GOTOOLCHAIN=local
COPY middleware/ ./
COPY docker/healthcheck.go /src/healthcheck.go
RUN mkdir -p /out /run/secrets \
 && go build -trimpath -buildvcs=false -tags=timetzdata -ldflags="-s -w -buildid=" -o /out/middleware ./cmd/middleware \
 && go build -trimpath -buildvcs=false -tags=timetzdata -ldflags="-s -w -buildid=" -o /out/healthcheck /src/healthcheck.go

FROM scratch
ARG BUILD_DATE
ARG REVISION
ARG VERSION
LABEL org.opencontainers.image.created=$BUILD_DATE \
      org.opencontainers.image.revision=$REVISION \
      org.opencontainers.image.source="https://github.com/carsupper665/Stock-Ai" \
      org.opencontainers.image.title="Stock AI Middleware" \
      org.opencontainers.image.version=$VERSION
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/middleware /middleware
COPY --from=build /out/healthcheck /healthcheck
COPY --from=build /run/secrets /run/secrets
USER 65532:65532
EXPOSE 4180
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/healthcheck", "http://127.0.0.1:4180/healthz"]
ENTRYPOINT ["/middleware"]
