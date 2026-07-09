# ---------- build ----------
FROM golang:1.24-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 go build -trimpath -o /out/duckdb-api .

# ---------- runtime ----------
FROM debian:bookworm-slim
# Non-root runtime identity, passed in from .env via compose build args. Used
# numerically (USER uid:gid), so no user account needs to be created and any
# uid works — pick one matching the Docker host user for bind mounts.
ARG USER_UID=1000
ARG GROUP_GID=1000
ARG USER_NAME=app
ARG GROUP_NAME=app

# Create the group/user with the requested GID/UID. If the id is already
# taken in the base image, rename that account instead — groupadd/useradd
# would fail with exit code 4/9.
RUN set -e; \
    existing_group="$(getent group ${GROUP_GID} | cut -d: -f1)"; \
    if [ -z "$existing_group" ]; then groupadd -g ${GROUP_GID} ${GROUP_NAME}; \
    elif [ "$existing_group" != "${GROUP_NAME}" ]; then groupmod -n ${GROUP_NAME} "$existing_group"; fi; \
    existing_user="$(getent passwd ${USER_UID} | cut -d: -f1)"; \
    if [ -z "$existing_user" ]; then useradd -m -u ${USER_UID} -g ${GROUP_GID} -s /bin/bash ${USER_NAME}; \
    elif [ "$existing_user" != "${USER_NAME}" ]; then \
        usermod -l ${USER_NAME} -d /home/${USER_NAME} -m "$existing_user"; \
        usermod -g ${GROUP_GID} ${USER_NAME}; \
    fi

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
# Pre-create /data and /local with the right owner so freshly-created named
# volumes inherit it.
RUN mkdir -p /home/${USER_NAME} /data /local \
    && chown -R ${USER_UID}:${GROUP_GID} /home/${USER_NAME} /data /local
COPY --from=build /out/duckdb-api /usr/local/bin/duckdb-api
ENV DATA_DIR=/data \
    PORT=8080 \
    HOME=/home/${USER_NAME}
VOLUME /data
EXPOSE 8080
USER ${USER_UID}:${GROUP_GID}
ENTRYPOINT ["/usr/local/bin/duckdb-api"]
