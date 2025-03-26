package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	_ "github.com/lib/pq"
)

// Configuration from environment variables
type Config struct {
	// PostgreSQL connection details
	PGHost     string
	PGPort     string
	PGUser     string
	PGPassword string
	PGDatabase string

	// Exoquic configuration
	ReplicationUser     string
	ReplicationPassword string
	PublicationName     string
	SlotName            string
	TablesToCapture     []string // Empty means all tables

	// Exoquic cloud connection
	ExoquicAPIKey   string
	ExoquicCloudURL string
}

func loadConfig() Config {
	// Set defaults and then override with environment variables
	config := Config{
		PGHost:              os.Getenv("PGHOST"),
		PGPort:              os.Getenv("PGPORT"),
		PGUser:              os.Getenv("PGUSER"),
		PGPassword:          os.Getenv("PGPASSWORD"),
		PGDatabase:          os.Getenv("PGDATABASE"),
		ReplicationUser:     os.Getenv("EXOQUIC_REPLICATION_USER"),
		ReplicationPassword: os.Getenv("EXOQUIC_REPLICATION_PASSWORD"),
		PublicationName:     os.Getenv("EXOQUIC_PUBLICATION_NAME"),
		SlotName:            os.Getenv("EXOQUIC_SLOT_NAME"),
		ExoquicAPIKey:       os.Getenv("EXOQUIC_API_KEY"),
		ExoquicCloudURL:     os.Getenv("EXOQUIC_CLOUD_URL"),
	}

	// Set defaults for empty values
	if config.PGPort == "" {
		config.PGPort = "5432"
	}
	if config.ReplicationUser == "" {
		config.ReplicationUser = "exoquic_replication"
	}
	if config.PublicationName == "" {
		config.PublicationName = "exoquic_publication"
	}
	if config.SlotName == "" {
		config.SlotName = "exoquic_replication_slot"
	}
	if config.ExoquicCloudURL == "" {
		config.ExoquicCloudURL = "https://api.exoquic.com"
	}

	// Parse tables to capture
	tablesStr := os.Getenv("TABLES_TO_CAPTURE")
	if tablesStr != "" {
		config.TablesToCapture = strings.Split(tablesStr, ",")
		// Trim whitespace from table names
		for i, table := range config.TablesToCapture {
			config.TablesToCapture[i] = strings.TrimSpace(table)
		}
	}

	return config
}

func validateConfig(config Config) error {
	if config.PGHost == "" {
		return fmt.Errorf("PGHOST environment variable is required")
	}
	if config.PGUser == "" {
		return fmt.Errorf("PGUSER environment variable is required")
	}
	if config.PGPassword == "" {
		return fmt.Errorf("PGPASSWORD environment variable is required")
	}
	if config.PGDatabase == "" {
		return fmt.Errorf("PGDATABASE environment variable is required")
	}
	if config.ReplicationPassword == "" {
		return fmt.Errorf("EXOQUIC_REPLICATION_PASSWORD environment variable is required")
	}
	return nil
}

// Connect to PostgreSQL with retry logic
func connectWithRetry(config Config) (*sql.DB, error) {
	connStr := fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		config.PGHost, config.PGPort, config.PGUser, config.PGPassword, config.PGDatabase,
	)

	var db *sql.DB
	var err error
	maxRetries := 5
	retryInterval := time.Second * 3

	for i := 0; i < maxRetries; i++ {
		log.Printf("Attempting to connect to PostgreSQL (attempt %d/%d)...", i+1, maxRetries)
		db, err = sql.Open("postgres", connStr)
		if err == nil {
			err = db.Ping()
			if err == nil {
				log.Println("Successfully connected to PostgreSQL")
				return db, nil
			}
		}

		log.Printf("Failed to connect: %v. Retrying in %v...", err, retryInterval)
		time.Sleep(retryInterval)
		// Increase interval for next retry
		retryInterval = retryInterval * 2
	}

	return nil, fmt.Errorf("failed to connect after %d attempts: %v", maxRetries, err)
}

// Check if the user has superuser privileges
func checkSuperuserPrivileges(db *sql.DB) (bool, error) {
	var isSuperuser bool
	err := db.QueryRow("SELECT usesuper FROM pg_user WHERE usename = current_user").Scan(&isSuperuser)
	if err != nil {
		return false, fmt.Errorf("failed to check superuser privileges: %v", err)
	}
	return isSuperuser, nil
}

// Configure WAL settings for logical replication
func configureWAL(db *sql.DB) (string, error) {
	var result strings.Builder
	var restartRequired bool

	// Check and set wal_level
	var walLevel string
	err := db.QueryRow("SHOW wal_level").Scan(&walLevel)
	if err != nil {
		return "", fmt.Errorf("failed to check wal_level: %v", err)
	}

	if walLevel != "logical" {
		_, err = db.Exec("ALTER SYSTEM SET wal_level = 'logical'")
		if err != nil {
			result.WriteString(fmt.Sprintf("ERROR: Failed to set wal_level to logical: %v\n", err))
		} else {
			// Reload pg configs so that we can modify the replication slots.
			db.Exec("SELECT pg_reload_conf()")
			result.WriteString(fmt.Sprintf("CHANGED: wal_level from '%s' to 'logical'.\n", walLevel))
			restartRequired = true
		}
	} else {
		result.WriteString("INFO: wal_level is correctly set to logical.\n")
	}

	// Check and set max_replication_slots
	var maxReplicationSlots int
	err = db.QueryRow("SHOW max_replication_slots").Scan(&maxReplicationSlots)
	if err != nil {
		return "", fmt.Errorf("failed to check max_replication_slots: %v", err)
	}

	if maxReplicationSlots < 5 {
		_, err = db.Exec("ALTER SYSTEM SET max_replication_slots = '5'")
		if err != nil {
			result.WriteString(fmt.Sprintf("ERROR: Failed to set max_replication_slots to 5: %v\n", err))
		} else {
			result.WriteString(fmt.Sprintf("CHANGED: max_replication_slots from %d to 5.\n", maxReplicationSlots))
			restartRequired = true
		}
	} else {
		result.WriteString(fmt.Sprintf("INFO: max_replication_slots is sufficient: %d.\n", maxReplicationSlots))
	}

	// Check and set max_wal_senders
	var maxWalSenders int
	err = db.QueryRow("SHOW max_wal_senders").Scan(&maxWalSenders)
	if err != nil {
		return "", fmt.Errorf("failed to check max_wal_senders: %v", err)
	}

	if maxWalSenders < 5 {
		_, err = db.Exec("ALTER SYSTEM SET max_wal_senders = '5'")
		if err != nil {
			result.WriteString(fmt.Sprintf("ERROR: Failed to set max_wal_senders to 5: %v\n", err))
		} else {
			result.WriteString(fmt.Sprintf("CHANGED: max_wal_senders from %d to 5.\n", maxWalSenders))
			restartRequired = true
		}
	} else {
		result.WriteString(fmt.Sprintf("INFO: max_wal_senders is sufficient: %d.\n", maxWalSenders))
	}

	// Apply changes if any were made
	if restartRequired {
		_, err = db.Exec("SELECT pg_reload_conf()")
		if err != nil {
			result.WriteString(fmt.Sprintf("ERROR: Failed to reload PostgreSQL configuration: %v\n", err))
		} else {
			result.WriteString("\nINFO: PostgreSQL configuration reloaded.\n")
		}

		result.WriteString("\nWARNING: Some changes require a server restart to take effect.\n")
		result.WriteString("To restart PostgreSQL, you may need to run:\n")
		result.WriteString("  - For systemd: sudo systemctl restart postgresql\n")
		result.WriteString("  - For Docker: docker restart <container_name>\n")
		result.WriteString("  - For Railway.app: Redeploy the PostgreSQL service\n")
	}

	return result.String(), nil
}

// Create replication user
func createReplicationUser(db *sql.DB, username, password string) (string, error) {
	var result strings.Builder

	// Check if user exists
	var userExists bool
	err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname = $1)", username).Scan(&userExists)
	if err != nil {
		return "", fmt.Errorf("failed to check if user exists: %v", err)
	}

	if userExists {
		result.WriteString(fmt.Sprintf("Replication user %s already exists.\n", username))
	} else {
		// Create the user
		_, err = db.Exec(fmt.Sprintf("CREATE ROLE %s WITH LOGIN PASSWORD '%s' REPLICATION", username, password))
		if err != nil {
			return "", fmt.Errorf("failed to create replication user: %v", err)
		}
		result.WriteString(fmt.Sprintf("Created replication user %s.\n", username))
	}

	// Grant permissions
	_, err = db.Exec(fmt.Sprintf("GRANT USAGE ON SCHEMA public TO %s", username))
	if err != nil {
		return "", fmt.Errorf("failed to grant usage permission: %v", err)
	}

	_, err = db.Exec(fmt.Sprintf("GRANT SELECT ON ALL TABLES IN SCHEMA public TO %s", username))
	if err != nil {
		return "", fmt.Errorf("failed to grant select permission: %v", err)
	}

	_, err = db.Exec(fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO %s", username))
	if err != nil {
		return "", fmt.Errorf("failed to alter default privileges: %v", err)
	}

	result.WriteString(fmt.Sprintf("Granted SELECT permissions to %s on all tables.\n", username))
	return result.String(), nil
}

// Create publication
func createPublication(db *sql.DB, publicationName string, tables []string) (string, error) {
	var result strings.Builder

	// Check if publication exists
	var publicationExists bool
	err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM pg_publication WHERE pubname = $1)", publicationName).Scan(&publicationExists)
	if err != nil {
		return "", fmt.Errorf("failed to check if publication exists: %v", err)
	}

	if publicationExists {
		result.WriteString(fmt.Sprintf("Publication %s already exists.\n", publicationName))

		// Drop and recreate the publication
		_, err = db.Exec(fmt.Sprintf("DROP PUBLICATION %s", publicationName))
		if err != nil {
			return "", fmt.Errorf("failed to drop existing publication: %v", err)
		}
		result.WriteString("Dropped existing publication to recreate it.\n")
	}

	// Create the publication
	var createCmd string
	if len(tables) == 0 {
		createCmd = fmt.Sprintf("CREATE PUBLICATION %s FOR ALL TABLES", publicationName)
	} else {
		createCmd = fmt.Sprintf("CREATE PUBLICATION %s FOR TABLE %s", publicationName, strings.Join(tables, ", "))
	}

	_, err = db.Exec(createCmd)
	if err != nil {
		return "", fmt.Errorf("failed to create publication: %v", err)
	}

	result.WriteString(fmt.Sprintf("Created publication %s.\n", publicationName))
	return result.String(), nil
}

// Create replication slot
func createReplicationSlot(db *sql.DB, slotName string) (string, error) {
	var result strings.Builder

	// Check if slot exists
	var slotExists bool
	err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM pg_replication_slots WHERE slot_name = $1)", slotName).Scan(&slotExists)
	if err != nil {
		return "", fmt.Errorf("failed to check if replication slot exists: %v", err)
	}

	if slotExists {
		result.WriteString(fmt.Sprintf("Replication slot %s already exists.\n", slotName))
	} else {
		// Create the slot
		_, err = db.Exec(fmt.Sprintf("SELECT pg_create_logical_replication_slot('%s', 'pgoutput')", slotName))
		if err != nil {
			return "", fmt.Errorf("failed to create replication slot: %v", err)
		}
		result.WriteString(fmt.Sprintf("Created logical replication slot %s.\n", slotName))
	}

	return result.String(), nil
}

// Set REPLICA IDENTITY FULL for tables without primary keys
func setReplicaIdentityFull(db *sql.DB) (string, error) {
	var result strings.Builder

	rows, err := db.Query(`
		SELECT n.nspname, c.relname
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relkind = 'r' 
			AND n.nspname = 'public'
			AND NOT EXISTS (
				SELECT 1 FROM pg_constraint
				WHERE conrelid = c.oid AND contype = 'p'
			)
	`)
	if err != nil {
		return "", fmt.Errorf("failed to query tables without primary keys: %v", err)
	}
	defer rows.Close()

	tablesModified := false
	for rows.Next() {
		var schemaName, tableName string
		if err := rows.Scan(&schemaName, &tableName); err != nil {
			return "", fmt.Errorf("failed to scan row: %v", err)
		}

		_, err := db.Exec(fmt.Sprintf("ALTER TABLE %s.%s REPLICA IDENTITY FULL", schemaName, tableName))
		if err != nil {
			result.WriteString(fmt.Sprintf("Failed to set REPLICA IDENTITY FULL for %s.%s: %v\n", schemaName, tableName, err))
		} else {
			result.WriteString(fmt.Sprintf("Set REPLICA IDENTITY FULL for %s.%s\n", schemaName, tableName))
			tablesModified = true
		}
	}

	if err := rows.Err(); err != nil {
		return result.String(), fmt.Errorf("error iterating over rows: %v", err)
	}

	if !tablesModified {
		result.WriteString("No tables required REPLICA IDENTITY FULL setting.\n")
	}

	return result.String(), nil
}

// Generate connection info
func generateConnectionInfo(db *sql.DB, config Config) (string, error) {
	var listenAddresses, port string

	err := db.QueryRow("SHOW listen_addresses").Scan(&listenAddresses)
	if err != nil {
		return "", fmt.Errorf("failed to get listen_addresses: %v", err)
	}

	err = db.QueryRow("SHOW port").Scan(&port)
	if err != nil {
		return "", fmt.Errorf("failed to get port: %v", err)
	}

	// If listen_addresses is '*' or includes multiple addresses, use the connection host
	if listenAddresses == "*" || strings.Contains(listenAddresses, ",") {
		listenAddresses = config.PGHost
	}

	connectionInfo := fmt.Sprintf(`
Exoquic Connection Information:
===========================
Host: %s
Port: %s
Database: %s
Username: %s
Replication Slot: %s
Publication: %s

Use these details to configure your Exoquic agent.
`, listenAddresses, port, config.PGDatabase, config.ReplicationUser, config.SlotName, config.PublicationName)

	return connectionInfo, nil
}

// Check tables that need primary keys
func checkTablePrimaryKeys(db *sql.DB) (string, error) {
	var result strings.Builder

	result.WriteString("\nTables without primary keys:\n")
	result.WriteString("-----------------------------\n")

	rows, err := db.Query(`
		SELECT n.nspname AS schema_name, c.relname AS table_name
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relkind = 'r'
			AND n.nspname = 'public'
			AND NOT EXISTS (
				SELECT 1 FROM pg_constraint
				WHERE conrelid = c.oid AND contype = 'p'
			)
	`)
	if err != nil {
		return "", fmt.Errorf("failed to query tables without primary keys: %v", err)
	}
	defer rows.Close()

	tablesFound := false
	for rows.Next() {
		var schemaName, tableName string
		if err := rows.Scan(&schemaName, &tableName); err != nil {
			return "", fmt.Errorf("failed to scan row: %v", err)
		}

		result.WriteString(fmt.Sprintf("- %s.%s (REPLICA IDENTITY FULL has been set)\n", schemaName, tableName))
		tablesFound = true
	}

	if err := rows.Err(); err != nil {
		return result.String(), fmt.Errorf("error iterating over rows: %v", err)
	}

	if !tablesFound {
		result.WriteString("No tables without primary keys found.\n")
	} else {
		result.WriteString("\nNote: For tables without primary keys, REPLICA IDENTITY FULL has been set\n")
		result.WriteString("to ensure all column values are included in change events. For better\n")
		result.WriteString("performance, consider adding primary keys to these tables.\n")
	}

	return result.String(), nil
}

// Register with Exoquic cloud (if API key is provided)
func registerWithExoquic(config Config, connectionInfo string) (string, error) {
	if config.ExoquicAPIKey == "" {
		return "Skipping Exoquic cloud registration (no API key provided).\n", nil
	}

	// Prepare connection details to send to the API
	type ConnectionDetails struct {
		Host            string `json:"host"`
		Port            string `json:"port"`
		Database        string `json:"database"`
		Username        string `json:"username"`
		Password        string `json:"password"`
		ReplicationSlot string `json:"replication_slot"`
		Publication     string `json:"publication"`
		ApiKey          string `json:"api_key"`
	}

	connDetails := ConnectionDetails{
		Host:            config.PGHost,
		Port:            config.PGPort,
		Database:        config.PGDatabase,
		Username:        config.ReplicationUser,
		Password:        config.ReplicationPassword,
		ReplicationSlot: config.SlotName,
		Publication:     config.PublicationName,
		ApiKey:          config.ExoquicAPIKey,
	}

	// Convert to JSON
	jsonData, err := json.Marshal(connDetails)
	if err != nil {
		return "", fmt.Errorf("failed to serialize connection details: %v", err)
	}

	// Send to Exoquic API
	apiURL := "https://db.exoquic.com/api/postgres"
	req, err := http.NewRequest("POST", apiURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("failed to create API request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", config.ExoquicAPIKey)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send connection details to Exoquic API: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API registration failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	return fmt.Sprintf("Successfully registered database with Exoquic"), nil
}

// Create Exoquic schema and functions
func createExoquicSchema(db *sql.DB) error {
	// Check if schema exists
	var schemaExists bool
	err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM pg_namespace WHERE nspname = 'exoquic')").Scan(&schemaExists)
	if err != nil {
		return fmt.Errorf("failed to check if schema exists: %v", err)
	}

	if !schemaExists {
		_, err = db.Exec("CREATE SCHEMA exoquic")
		if err != nil {
			return fmt.Errorf("failed to create exoquic schema: %v", err)
		}
	}

	// Create exoquic.status view
	_, err = db.Exec(`
		CREATE OR REPLACE VIEW exoquic.status AS
		SELECT 
			current_database() AS database_name,
			(SELECT count(*) FROM pg_publication) AS publication_count,
			(SELECT count(*) FROM pg_replication_slots) AS replication_slot_count,
			(SELECT count(*) FROM pg_stat_replication) AS active_replication_count;
	`)
	if err != nil {
		return fmt.Errorf("failed to create status view: %v", err)
	}

	return nil
}

func main() {
	log.Println("Starting Exoquic PostgreSQL Configurator for Railway.app")

	// Load configuration from environment variables
	config := loadConfig()

	// Validate configuration
	if err := validateConfig(config); err != nil {
		log.Fatalf("Configuration error: %v", err)
	}

	// Connect to PostgreSQL with retry
	db, err := connectWithRetry(config)
	if err != nil {
		log.Fatalf("Failed to connect to PostgreSQL: %v", err)
	}
	defer db.Close()

	db.SetConnMaxLifetime(time.Minute * 3)

	// Check superuser privileges
	isSuperuser, err := checkSuperuserPrivileges(db)
	if err != nil {
		log.Fatalf("Error checking privileges: %v", err)
	}

	if !isSuperuser {
		log.Println("ERROR: Current user does not have superuser privileges.")
		os.Exit(1)
	}

	var output strings.Builder
	output.WriteString("Exoquic PostgreSQL Configuration Report\n")
	output.WriteString("=====================================\n\n")

	// Configure WAL settings
	walConfig, err := configureWAL(db)
	if err != nil {
		log.Printf("Warning: Error configuring WAL settings: %v", err)
	} else {
		output.WriteString("WAL Configuration:\n")
		output.WriteString("------------------\n")
		output.WriteString(walConfig)
		output.WriteString("\n")
	}

	// Create Exoquic schema and functions
	err = createExoquicSchema(db)
	if err != nil {
		log.Printf("Warning: Error creating Exoquic schema: %v", err)
	} else {
		output.WriteString("Created Exoquic schema and helper objects.\n\n")
	}

	// Create replication user
	userResult, err := createReplicationUser(db, config.ReplicationUser, config.ReplicationPassword)
	if err != nil {
		log.Printf("Warning: Error creating replication user: %v", err)
	} else {
		output.WriteString("Replication User:\n")
		output.WriteString("----------------\n")
		output.WriteString(userResult)
		output.WriteString("\n")
	}

	// Create publication
	pubResult, err := createPublication(db, config.PublicationName, config.TablesToCapture)
	if err != nil {
		log.Printf("Warning: Error creating publication: %v", err)
	} else {
		output.WriteString("Publication:\n")
		output.WriteString("-----------\n")
		output.WriteString(pubResult)
		output.WriteString("\n")
	}

	// Create replication slot
	slotResult, err := createReplicationSlot(db, config.SlotName)
	if err != nil {
		log.Printf("Warning: Error creating replication slot: %v", err)
	} else {
		output.WriteString("Replication Slot:\n")
		output.WriteString("----------------\n")
		output.WriteString(slotResult)
		output.WriteString("\n")
	}

	// Set REPLICA IDENTITY FULL for tables without primary keys
	replicaResult, err := setReplicaIdentityFull(db)
	if err != nil {
		log.Printf("Warning: Error setting REPLICA IDENTITY: %v", err)
	} else {
		output.WriteString("Replica Identity:\n")
		output.WriteString("----------------\n")
		output.WriteString(replicaResult)
		output.WriteString("\n")
	}

	// Check tables that need primary keys
	tableCheck, err := checkTablePrimaryKeys(db)
	if err != nil {
		log.Printf("Warning: Error checking table primary keys: %v", err)
	} else {
		output.WriteString(tableCheck)
		output.WriteString("\n")
	}

	// Generate connection info
	connectionInfo, err := generateConnectionInfo(db, config)
	if err != nil {
		log.Printf("Warning: Error generating connection info: %v", err)
	} else {
		output.WriteString(connectionInfo)
		output.WriteString("\n")
	}

	// Register with Exoquic cloud if API key is provided
	if config.ExoquicAPIKey != "" {
		cloudResult, err := registerWithExoquic(config, connectionInfo)
		if err != nil {
			log.Printf("Warning: Error registering with Exoquic cloud: %v", err)
		} else {
			output.WriteString("Exoquic Cloud Registration:\n")
			output.WriteString("--------------------------\n")
			output.WriteString(cloudResult)
			output.WriteString("\n")
		}
	}

	log.Println("Configuration complete!")
	fmt.Println("\n" + output.String())
	log.Println("Configuration successful. Service will exit in 5 minutes.")
	log.Println("You can safely deploy this Railway service again when needed.")
}
