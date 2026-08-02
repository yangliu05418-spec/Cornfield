# syntax=docker/dockerfile:1.7
FROM node:24.15-alpine@sha256:d1b3b4da11eefd5941e7f0b9cf17783fc99d9c6fc34884a665f40a06dbdfc94f AS build
RUN corepack enable
WORKDIR /src
COPY web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml ./
RUN --mount=type=cache,target=/root/.local/share/pnpm/store pnpm install --frozen-lockfile
COPY web/ .
RUN pnpm build && test -f dist/client/_shell.html

FROM node:24.15-alpine@sha256:d1b3b4da11eefd5941e7f0b9cf17783fc99d9c6fc34884a665f40a06dbdfc94f AS director-build
WORKDIR /director
COPY 3d-director-desk-main/package.json 3d-director-desk-main/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci
COPY 3d-director-desk-main/ ./
RUN npm run build:embedded && test -f dist/index.html

FROM nginx:1.29-alpine@sha256:5616878291a2eed594aee8db4dade5878cf7edcb475e59193904b198d9b830de
COPY web/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /src/dist/client/ /usr/share/nginx/html/
COPY --from=director-build /director/dist/ /usr/share/nginx/html/director-desk/
RUN apk upgrade --no-cache && \
    sed -i -E 's#^[[:space:]]*pid[[:space:]]+[^;]+;#pid /tmp/nginx.pid;#' /etc/nginx/nginx.conf && \
    grep -Eq '^pid[[:space:]]+/tmp/nginx[.]pid;$' /etc/nginx/nginx.conf
USER 101:101
EXPOSE 8080
