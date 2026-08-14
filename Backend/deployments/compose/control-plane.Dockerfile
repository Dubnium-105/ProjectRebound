FROM golang:1.25.13-bookworm AS build
ARG GOPROXY=https://proxy.golang.org,direct
ARG GOSUMDB=sum.golang.org
ENV GOPROXY=${GOPROXY}
ENV GOSUMDB=${GOSUMDB}
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/control-plane ./cmd/control-plane
RUN CGO_ENABLED=1 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/decrypt-ticket ./cmd/decrypt-ticket
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/test-ticket-verifier ./cmd/test-ticket-verifier

FROM debian:bookworm-slim AS runtime-common
RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates && \
    rm -rf /var/lib/apt/lists/* && \
    groupadd --system --gid 999 app && \
    useradd --system --uid 999 --gid app --no-create-home app
COPY --from=build /out/control-plane /control-plane
COPY --from=build /out/decrypt-ticket /usr/local/bin/decrypt-ticket
COPY --from=build /src/deployments/updates /deployments/updates
COPY --from=build /src/deployments/control-plane/toolbox-signer.pem /usr/share/projectrebound/toolbox-signer.pem
USER app
EXPOSE 8080
ENTRYPOINT ["/control-plane"]

FROM runtime-common AS integration
COPY --chmod=0555 --from=build /out/test-ticket-verifier /test-tools/decrypt-ticket

FROM runtime-common AS runtime
