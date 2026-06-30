# Single-stage image with Go + build deps
# Source code is bind-mounted from host at runtime
FROM golang:1.23-alpine

RUN apk add --no-cache gcc musl-dev ca-certificates sqlite-libs docker-cli

# Create workdir owned by non-root user so we can build without root
RUN mkdir -p /app && chmod 777 /app
WORKDIR /app

# Build happens at container start via entrypoint
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

EXPOSE 8080
ENTRYPOINT ["/entrypoint.sh"]
