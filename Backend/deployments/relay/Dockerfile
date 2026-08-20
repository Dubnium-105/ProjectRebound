FROM golang:1.26.6-alpine AS build
ARG GOPROXY=https://proxy.golang.org,direct
ARG GOSUMDB=sum.golang.org
ENV GOPROXY=${GOPROXY}
ENV GOSUMDB=${GOSUMDB}
RUN apk add --no-cache ca-certificates
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN mkdir -p /out/data /out/config && \
    cp config.edge-relay.yaml /out/config/config.edge-relay.yaml && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/edge-relay ./cmd/edge-relay

FROM scratch
COPY --from=build --chown=65532:65532 /out/edge-relay /edge-relay
COPY --from=build --chown=65532:65532 /out/config /etc/projectrebound
COPY --from=build --chown=65532:65532 /out/data /edge-relay-data
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
USER 65532:65532
EXPOSE 8443/udp 9100/tcp
ENTRYPOINT ["/edge-relay"]
CMD ["-config", "/etc/projectrebound/config.edge-relay.yaml"]
