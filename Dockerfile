# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.26 AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS
ARG TARGETARCH
ENV CGO_ENABLED=0
RUN GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    go build -trimpath -ldflags="-s -w" -o /out/belepteto .

# Keep the database directory writable while still running as a numeric
# non-root user in the scratch image.
RUN mkdir -p /out/data && chown 65532:65532 /out/data

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/belepteto /belepteto
COPY --from=build --chown=65532:65532 /out/data /data

ENV BELEPTETO_DB_PATH=/data/belepteto.db
EXPOSE 8080
EXPOSE 1883
VOLUME ["/data"]
USER 65532:65532
ENTRYPOINT ["/belepteto"]
