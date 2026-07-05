# Metabase + DuckDB community driver (motherduckdb/metabase_duckdb_driver).
#
# The official metabase/metabase image is Alpine (musl libc); DuckDB's JDBC
# driver ships a glibc-linked native library that crashes the JVM there. So we
# take the metabase.jar from the official image and run it on a glibc JRE.
#
# Driver releases are versioned <duckdb-version>.<patch>; check
# https://github.com/motherduckdb/metabase_duckdb_driver/releases for the
# newest driver and its supported Metabase version before upgrading.
ARG METABASE_VERSION=latest
FROM metabase/metabase:${METABASE_VERSION} AS official

FROM eclipse-temurin:21-jre-noble
RUN apt-get update \
    && apt-get install -y --no-install-recommends curl \
    && rm -rf /var/lib/apt/lists/*
COPY --from=official /app/metabase.jar /app/metabase.jar

ARG DUCKDB_DRIVER_VERSION=1.5.3.0
ADD --chmod=644 \
    https://github.com/motherduckdb/metabase_duckdb_driver/releases/download/${DUCKDB_DRIVER_VERSION}/duckdb.metabase-driver.jar \
    /plugins/duckdb.metabase-driver.jar

ENV MB_PLUGINS_DIR=/plugins
WORKDIR /app
EXPOSE 3000
CMD ["java", "-jar", "/app/metabase.jar"]
