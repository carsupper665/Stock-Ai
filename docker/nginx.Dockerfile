FROM node:24.21.0-alpine3.23@sha256:9ec4a2e289874ed0d722e1772ec2de45d2801541db8612f3638b26f128c69ac2 AS frontend-build
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci --ignore-scripts
COPY frontend/index.html frontend/vite.config.js ./
COPY frontend/src ./src
RUN npm run build

FROM golang:1.27.2-alpine3.23@sha256:9e45f0eb4a63ed37ad6e604950407558b7c738e34e015ae77a5d4ad369cde079 AS go-build
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
RUN go build -trimpath -buildvcs=false -tags=timetzdata -ldflags="-s -w -buildid=" -o /out/console ./cmd/console \
 && go build -trimpath -buildvcs=false -tags=timetzdata -ldflags="-s -w -buildid=" -o /out/healthcheck /src/healthcheck.go

FROM scratch
ARG BUILD_DATE
ARG REVISION
ARG VERSION
LABEL org.opencontainers.image.created=$BUILD_DATE \
      org.opencontainers.image.revision=$REVISION \
      org.opencontainers.image.source="https://github.com/carsupper665/Stock-Ai" \
      org.opencontainers.image.title="Stock AI Console" \
      org.opencontainers.image.version=$VERSION
COPY --from=go-build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=go-build /out/console /console
COPY --from=go-build /out/healthcheck /healthcheck
COPY --from=frontend-build /src/frontend/dist /public
ENV CONSOLE_ADDR=0.0.0.0:8088 CONSOLE_PUBLIC_DIR=/public
EXPOSE 8088
USER 65532:65532
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/healthcheck", "http://127.0.0.1:8088/"]
ENTRYPOINT ["/console"]
