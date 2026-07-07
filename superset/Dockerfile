# Apache Superset + DuckDB SQLAlchemy driver (duckdb-engine).
ARG SUPERSET_VERSION=latest
FROM apache/superset:${SUPERSET_VERSION}

# Superset runs from the /app/.venv virtualenv — install the driver there
# (a plain `pip install` would land in the unused system Python).
USER root
RUN uv pip install --python /app/.venv/bin/python --no-cache duckdb duckdb-engine
# Pre-create DuckDB's home dirs owned by the superset user, so the named
# volumes mounted here inherit that ownership and extension auto-install works.
RUN mkdir -p /app/superset_home/.duckdb/stored_secrets \
    && chown -R superset /app/superset_home/.duckdb
USER superset
