# Exoquic PostgreSQL Configurator

A utility for automatically configuring PostgreSQL databases with Exoquic datastreaming platform.

## Overview

This tool automates the process of setting up PostgreSQL for logical replication, which is required for Exoquic. It handles all the necessary configuration changes, user creation, and replication setup to enable real-time data streaming from your PostgreSQL database.

## What This Tool Does

The Exoquic PostgreSQL Configurator performs the following tasks:

### 1. PostgreSQL Configuration Modifications

- **WAL Settings**: 
  - Sets `wal_level` to `logical` (required for logical replication)
  - Increases `max_replication_slots` to at least 5
  - Increases `max_wal_senders` to at least 5
  - Reloads PostgreSQL configuration after changes

### 2. Database Object Creation

- **Replication User**:
  - Creates a dedicated user with replication privileges
  - Grants necessary permissions for CDC operations

- **Publication**:
  - Creates a PostgreSQL publication that defines which tables to replicate
  - Can be configured for all tables or specific tables only

- **Replication Slot**:
  - Creates a logical replication slot that Exoquic uses to consume changes
  - Uses the pgoutput plugin for logical decoding

- **Exoquic Schema**:
  - Creates an `exoquic` schema with helper objects
  - Includes a status view for monitoring replication

### 3. Table Configuration

- **REPLICA IDENTITY FULL**:
  - Identifies tables without primary keys
  - Sets `REPLICA IDENTITY FULL` for these tables to ensure all column values are included in change events
  - Provides recommendations for adding primary keys for better performance

### 4. Exoquic Integration

- **Connection Information**:
  - Generates connection details needed for Exoquic configuration
  - Includes host, port, database, username, replication slot, and publication

- **Cloud Registration** (optional):
  - If an API key is provided, registers the database with Exoquic cloud
  - Sends necessary connection details securely to the Exoquic API

## How Exoquic Consumes Database Changes

After this tool completes its configuration:

1. **Change Tracking**: PostgreSQL's logical replication system tracks all data changes (inserts, updates, deletes) in the Write-Ahead Log (WAL).

2. **Logical Decoding**: The changes are decoded from the binary WAL format into a logical representation using the pgoutput plugin.

3. **Replication Slot**: The created replication slot maintains a pointer to the last consumed WAL position, ensuring no changes are missed, even if Exoquic is temporarily disconnected.

4. **Publication**: The publication defines which tables' changes should be captured and made available for consumption.

5. **Consumption**: Exoquic connects to PostgreSQL using the replication user credentials and reads changes from the replication slot, processing them according to your configuration.

6. **Data Delivery**: Exoquic then delivers these events to your authorized Kafka, MQTT, AMQP, STOMP, Websocket and HTTP clients.

## Environment Variables

### Required Variables

- `PGHOST`: PostgreSQL host address
- `PGUSER`: PostgreSQL admin username (requires superuser privileges)
- `PGPASSWORD`: PostgreSQL admin password
- `PGDATABASE`: PostgreSQL database name
- `EXOQUIC_REPLICATION_PASSWORD`: Password for the replication user

### Optional Variables

- `PGPORT`: PostgreSQL port (default: 5432)
- `EXOQUIC_REPLICATION_USER`: Username for the replication user (default: exoquic_replication)
- `EXOQUIC_PUBLICATION_NAME`: Name of the publication (default: exoquic_publication)
- `EXOQUIC_SLOT_NAME`: Name of the replication slot (default: exoquic_replication_slot)
- `EXOQUIC_API_KEY`: API key for Exoquic cloud registration (optional)
- `EXOQUIC_CLOUD_URL`: URL for Exoquic cloud API (default: https://api.exoquic.com)
- `TABLES_TO_CAPTURE`: Comma-separated list of tables to include in the publication (default: all tables)

## Usage

### Running Locally

```bash
# Set required environment variables
export PGHOST=localhost
export PGUSER=postgres
export PGPASSWORD=your_password
export PGDATABASE=your_database
export EXOQUIC_REPLICATION_PASSWORD=replication_password

# Optional: Specify tables to capture
export TABLES_TO_CAPTURE="table1,table2,table3"

# Run the configurator
go run main.go
```

### Configuring your Postgres database in Railway

1. Deploy **exoquic-postgres-configurer** template
2. View runtime logs to make sure WAL settings have been modified.
3. Restart the postgres server for the WAL settings to apply.
4. You're good to go!

## Important Notes

- **Superuser Privileges**: The PostgreSQL user specified in `PGUSER` must have superuser privileges to modify WAL settings.
- **Server Restart**: Some WAL configuration changes require a PostgreSQL server restart to take effect.
- **Tables Without Primary Keys**: For optimal performance, it's recommended to add primary keys to all tables. Tables without primary keys will work but require more resources.
- **Security**: The replication user is created with minimal necessary permissions for CDC operations.

## Questions
if you have any questions, create an issue!

## License

[MIT License](LICENSE)
