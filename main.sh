#!/bin/bash
set -e

# Run the configurator
PGHOST=localhost \
PGPORT=5432 \
PGUSER=postgres \
PGPASSWORD=postgres \
PGDATABASE=exoquic_test \
EXOQUIC_REPLICATION_USER=exoquic_user \
EXOQUIC_REPLICATION_PASSWORD=exoquic_password \
EXOQUIC_API_KEY= \
EXOQUIC_ENV=dev \
go run main.go

echo "Done. To clean up, run: docker stop exoquic-postgres && docker rm exoquic-postgres"
