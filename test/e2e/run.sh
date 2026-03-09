#!/bin/bash

cd "$(dirname "$0")"

cleanup() {
    echo "Cleaning up..."
    docker-compose down -v
}
trap cleanup EXIT

echo "Starting e2e test environment..."
docker-compose up --build --abort-on-container-exit --exit-code-from e2e
