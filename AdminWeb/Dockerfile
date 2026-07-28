FROM node:24-alpine AS build

WORKDIR /src
COPY AdminWeb/package.json AdminWeb/package-lock.json ./
RUN npm ci
COPY AdminWeb/ ./
RUN npm run build

FROM golang:1.25.12-alpine AS caddy-build
ARG GOPROXY=https://proxy.golang.org,direct
ARG GOSUMDB=sum.golang.org
ENV GOPROXY=${GOPROXY}
ENV GOSUMDB=${GOSUMDB}
WORKDIR /src
COPY AdminWeb/caddy/go.mod AdminWeb/caddy/go.sum ./
RUN go mod download
COPY AdminWeb/caddy/main.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/caddy .

FROM scratch
COPY --from=caddy-build /out/caddy /usr/bin/caddy
COPY AdminWeb/Caddyfile /etc/caddy/Caddyfile
COPY --from=build --chown=10001:10001 /src/dist /srv

USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/usr/bin/caddy"]
CMD ["run", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile"]
