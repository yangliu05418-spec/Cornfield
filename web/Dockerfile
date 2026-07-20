# syntax=docker/dockerfile:1.7
FROM node:26.5-alpine@sha256:e88a35be04478413b7c71c455cd9865de9b9360e1f43456be5951032d7ac1a66 AS build
RUN corepack enable
WORKDIR /src
COPY web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml ./
RUN --mount=type=cache,target=/root/.local/share/pnpm/store pnpm install --frozen-lockfile
COPY web/ .
RUN pnpm build && test -f dist/client/_shell.html

FROM nginx:1.29-alpine@sha256:5616878291a2eed594aee8db4dade5878cf7edcb475e59193904b198d9b830de
COPY web/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /src/dist/client/ /usr/share/nginx/html/
RUN apk upgrade --no-cache && \
    sed -i -E 's#^[[:space:]]*pid[[:space:]]+[^;]+;#pid /tmp/nginx.pid;#' /etc/nginx/nginx.conf && \
    grep -Eq '^pid[[:space:]]+/tmp/nginx[.]pid;$' /etc/nginx/nginx.conf
USER 101:101
EXPOSE 8080
