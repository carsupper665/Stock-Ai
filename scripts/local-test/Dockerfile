FROM golang:1.27.2-alpine3.23@sha256:9e45f0eb4a63ed37ad6e604950407558b7c738e34e015ae77a5d4ad369cde079 AS build
WORKDIR /src
COPY go.mod ./go.mod
COPY fixture.go ./fixture.go
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o /local-fixture .

FROM scratch
COPY --from=build /local-fixture /local-fixture
USER 65532:65532
EXPOSE 9443 18080
HEALTHCHECK --interval=2s --timeout=1s --retries=15 CMD ["/local-fixture", "--healthcheck"]
ENTRYPOINT ["/local-fixture"]
