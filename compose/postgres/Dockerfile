# The development stack's PostgreSQL: the database the integration tier
# connects to through SQLATE_DSN. This FROM line is PostgreSQL's one pin.
FROM postgres:18.6-alpine

# The user, password, and database SQLATE_DSN names; development values, not
# secrets.
ENV POSTGRES_USER=app \
    POSTGRES_PASSWORD=app \
    POSTGRES_DB=app

# Healthy once the server accepts TCP connections to the app database. The
# probe goes over TCP, not the socket, because the first start's
# initialization runs a temporary server on the socket alone, which would
# report ready before the real server starts.
HEALTHCHECK --start-period=30s --start-interval=1s --interval=5s --timeout=3s --retries=5 \
    CMD ["pg_isready", "-h", "127.0.0.1", "-U", "app", "-d", "app"]
