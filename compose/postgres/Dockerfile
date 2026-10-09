# The integration tier's PostgreSQL. This FROM line is PostgreSQL's one pin.
FROM postgres:18.6-alpine

# The user, password, and database that SQLATE_DSN in mise.toml names.
ENV POSTGRES_USER=app \
    POSTGRES_PASSWORD=app \
    POSTGRES_DB=app

# Healthy once the server accepts TCP connections to the app database. The
# probe goes over TCP, not the socket, because the first start's
# initialization runs a temporary server on the socket alone, which would
# report ready before the real server starts.
HEALTHCHECK --start-period=60s --start-interval=1s --interval=10s --timeout=3s --retries=3 \
    CMD ["pg_isready", "--host=127.0.0.1", "--username=app", "--dbname=app"]
