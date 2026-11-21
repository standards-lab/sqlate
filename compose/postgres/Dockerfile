# See README.md
FROM postgres:19.0-alpine

ENV POSTGRES_USER=app \
    POSTGRES_PASSWORD=app \
    POSTGRES_DB=app

HEALTHCHECK --start-period=30s --start-interval=1s --interval=5s --timeout=3s --retries=5 \
    CMD ["pg_isready", "-h", "127.0.0.1", "-U", "app", "-d", "app"]
