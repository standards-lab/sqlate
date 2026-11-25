# See README.md
FROM postgres:19.0-alpine

ARG PGVECTOR_VERSION=0.8.7

ADD https://github.com/pgvector/pgvector.git#v${PGVECTOR_VERSION} /usr/src/pgvector

RUN apk add --no-cache --virtual .build-deps build-base \
    && cd /usr/src/pgvector \
    && make with_llvm=no OPTFLAGS="" \
    && make install with_llvm=no \
    && cd / \
    && rm -rf /usr/src/pgvector \
    && apk del .build-deps

ENV POSTGRES_USER=app \
    POSTGRES_PASSWORD=app \
    POSTGRES_DB=app

HEALTHCHECK --start-period=30s --start-interval=1s --interval=5s --timeout=3s --retries=5 \
    CMD ["pg_isready", "-h", "127.0.0.1", "-U", "app", "-d", "app"]
