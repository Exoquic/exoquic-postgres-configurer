#!/bin/bash
set -e

# Start PostgreSQL container with default settings
docker run --name exoquic-postgres \
  --network host \
	--rm \
  -e POSTGRES_USER=postgres \
  -e POSTGRES_PASSWORD=postgres \
  -e POSTGRES_DB=exoquic_test \
  -d postgres:15

# Wait for PostgreSQL to start up
sleep 5
echo "PostgreSQL is starting..."
until docker exec exoquic-postgres pg_isready -U postgres &> /dev/null; do
  sleep 1
done
echo "PostgreSQL is ready"

# Create test tables
docker exec -i exoquic-postgres psql -U postgres -d exoquic_test << EOF
CREATE TABLE test_data (id SERIAL, name TEXT, value INTEGER);
CREATE TABLE with_pk (id SERIAL PRIMARY KEY, name TEXT);

INSERT INTO test_data (name, value) VALUES ('Test 1', 100), ('Test 2', 200);
INSERT INTO with_pk (name) VALUES ('PK 1'), ('PK 2');
EOF

# Run the configurator
PGHOST=localhost \
PGPORT=5432 \
PGUSER=postgres \
PGPASSWORD=postgres \
PGDATABASE=exoquic_test \
EXOQUIC_REPLICATION_USER=exoquic_user \
EXOQUIC_REPLICATION_PASSWORD=exoquic_password \
go run main.go

echo "Done. To clean up, run: docker stop exoquic-postgres && docker rm exoquic-postgres"