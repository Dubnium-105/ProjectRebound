FROM golang:1.25.12-alpine AS build
ARG GOPROXY=https://proxy.golang.org,direct
ARG GOSUMDB=sum.golang.org
ENV GOPROXY=${GOPROXY}
ENV GOSUMDB=${GOSUMDB}
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/control-plane ./cmd/control-plane

FROM alpine:3.20
RUN addgroup -S app && adduser -S -G app app
COPY --from=build /out/control-plane /control-plane
COPY --from=build /src/deployments/updates /deployments/updates
USER app
EXPOSE 8080
ENTRYPOINT ["/control-plane"]
