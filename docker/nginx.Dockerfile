FROM node:24.21.0-alpine3.23@sha256:9ec4a2e289874ed0d722e1772ec2de45d2801541db8612f3638b26f128c69ac2 AS frontend-build
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci --ignore-scripts
COPY frontend/index.html frontend/vite.config.js frontend/VERSION ./
COPY frontend/src ./src
RUN npm run build

FROM nginx@sha256:0985e772fb9f729e6fa0980da05fca5d9c468e870eed43071545afa9d2e27d94
ARG BUILD_DATE
ARG REVISION
ARG SOURCE_DATE_EPOCH
ARG VERSION
RUN test -n "$BUILD_DATE" && test -n "$REVISION" && test -n "$SOURCE_DATE_EPOCH" && test -n "$VERSION"
RUN apk upgrade --no-cache libexpat libx11 pcre2 tiff
LABEL org.opencontainers.image.created=$BUILD_DATE \
      org.opencontainers.image.revision=$REVISION \
      org.opencontainers.image.source="https://github.com/carsupper665/Stock-Ai" \
      org.opencontainers.image.title="Stock AI Nginx" \
      org.opencontainers.image.version=$VERSION
COPY --from=frontend-build /src/frontend/dist /usr/share/nginx/html
