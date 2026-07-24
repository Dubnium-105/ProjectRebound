FROM node:24-alpine AS build

WORKDIR /src
COPY AdminWeb/package.json AdminWeb/package-lock.json ./
RUN npm ci
COPY AdminWeb/ ./
RUN npm run build

FROM caddy:2-alpine

RUN setcap -r /usr/bin/caddy \
    && addgroup -S -g 10001 caddy \
    && adduser -S -D -H -u 10001 -G caddy caddy

COPY AdminWeb/Caddyfile /etc/caddy/Caddyfile
COPY --from=build --chown=10001:10001 /src/dist /srv

USER caddy
EXPOSE 8080
