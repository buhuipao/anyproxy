package credential

import (
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"sync"

	// Database drivers for the documented driver names. All are pure Go, so CGO_ENABLED=0 builds keep working.
	"github.com/go-sql-driver/mysql" // registers "mysql"
	_ "github.com/lib/pq"            // registers "postgres"
	_ "modernc.org/sqlite"           // registers "sqlite"
)

// DBStore implements database-based credential storage
type DBStore struct {
	db            *sql.DB
	tableName     string
	dollarParams  bool // postgres takes $1, $2, ... instead of ?
	mu            sync.RWMutex
	preparedStmts map[string]*sql.Stmt
}

// DBConfig holds database configuration
type DBConfig struct {
	Driver     string // Database driver: mysql, postgres, sqlite
	DataSource string // Connection string
	TableName  string // Table name for credentials
}

// tableNameRegex validates table names to prevent SQL injection
var tableNameRegex = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// validateTableName ensures the table name is safe to use in SQL queries
func validateTableName(name string) error {
	if !tableNameRegex.MatchString(name) {
		return fmt.Errorf("invalid table name: must contain only letters, numbers, and underscores, and start with a letter or underscore")
	}
	return nil
}

// NewDBStore creates a new database-based credential store
func NewDBStore(config *DBConfig) (*DBStore, error) {
	if config.TableName == "" {
		config.TableName = "credentials"
	}

	// Validate table name to prevent SQL injection
	if err := validateTableName(config.TableName); err != nil {
		return nil, err
	}

	dataSource := config.DataSource
	if config.Driver == "mysql" {
		// Set counts matched rows, but MySQL counts only changed rows unless clientFoundRows is set
		mysqlConfig, err := mysql.ParseDSN(dataSource)
		if err != nil {
			return nil, fmt.Errorf("invalid mysql data_source: %v", err)
		}
		mysqlConfig.ClientFoundRows = true
		dataSource = mysqlConfig.FormatDSN()
	}

	db, err := sql.Open(config.Driver, dataSource)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %v", err)
	}

	// Test connection
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %v", err)
	}

	store := &DBStore{
		db:            db,
		tableName:     config.TableName,
		dollarParams:  config.Driver == "postgres",
		preparedStmts: make(map[string]*sql.Stmt),
	}

	// Create table if not exists
	if err := store.createTable(); err != nil {
		return nil, fmt.Errorf("failed to create table: %v", err)
	}

	// Prepare statements
	if err := store.prepareStatements(); err != nil {
		return nil, fmt.Errorf("failed to prepare statements: %v", err)
	}

	return store, nil
}

// bind rewrites the ? placeholders in query as $1, $2, ... for postgres
func (ds *DBStore) bind(query string) string {
	if !ds.dollarParams {
		return query
	}
	var b strings.Builder
	n := 0
	for _, r := range query {
		if r == '?' {
			n++
			fmt.Fprintf(&b, "$%d", n)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// createTable creates the credentials table if it doesn't exist
func (ds *DBStore) createTable() error {
	// Table name is validated in NewDBStore, safe to use in query
	query := fmt.Sprintf( // #nosec G201 - table name is validated
		`CREATE TABLE IF NOT EXISTS %s (
			group_id VARCHAR(255) PRIMARY KEY,
			password_hash VARCHAR(64) NOT NULL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`, ds.tableName)

	_, err := ds.db.Exec(query)
	return err
}

// prepareStatements prepares commonly used SQL statements
func (ds *DBStore) prepareStatements() error {
	// Prepare basic statements that work across all databases
	// Table name is validated in NewDBStore, safe to use in queries
	statements := map[string]string{
		"get": fmt.Sprintf( // #nosec G201 - table name is validated
			`SELECT password_hash FROM %s WHERE group_id = ?`, ds.tableName),
		"delete": fmt.Sprintf( // #nosec G201 - table name is validated
			`DELETE FROM %s WHERE group_id = ?`, ds.tableName),
	}

	for name, query := range statements {
		stmt, err := ds.db.Prepare(ds.bind(query))
		if err != nil {
			return fmt.Errorf("failed to prepare %s statement: %v", name, err)
		}
		ds.preparedStmts[name] = stmt
	}

	return nil
}

// Set stores or updates password (raw, not hashed)
func (ds *DBStore) Set(groupID string, password string) error {
	ds.mu.Lock()
	defer ds.mu.Unlock()

	// First try to update
	// Table name is validated in NewDBStore, safe to use in query
	updateQuery := fmt.Sprintf( // #nosec G201 - table name is validated
		`UPDATE %s SET password_hash = ?, updated_at = CURRENT_TIMESTAMP WHERE group_id = ?`,
		ds.tableName)

	result, err := ds.db.Exec(ds.bind(updateQuery), password, groupID)
	if err != nil {
		return fmt.Errorf("failed to update credentials: %v", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %v", err)
	}

	// If no rows were updated, insert new record
	if rowsAffected == 0 {
		// Table name is validated in NewDBStore, safe to use in query
		insertQuery := fmt.Sprintf( // #nosec G201 - table name is validated
			`INSERT INTO %s (group_id, password_hash) VALUES (?, ?)`,
			ds.tableName)

		_, err = ds.db.Exec(ds.bind(insertQuery), groupID, password)
		if err != nil {
			return fmt.Errorf("failed to insert credentials: %v", err)
		}
	}

	return nil
}

// SetIfAbsent inserts the password unless the group already has one, and returns the stored password.
// The primary key makes this safe for gateways that share the database.
func (ds *DBStore) SetIfAbsent(groupID string, password string) (string, error) {
	// Table name is validated in NewDBStore, safe to use in query
	insertQuery := fmt.Sprintf( // #nosec G201 - table name is validated
		`INSERT INTO %s (group_id, password_hash) VALUES (?, ?)`,
		ds.tableName)

	_, insertErr := ds.db.Exec(ds.bind(insertQuery), groupID, password)
	if insertErr == nil {
		return password, nil
	}

	// The insert fails when the group already has a row; return its password
	stored, err := ds.Get(groupID)
	if err != nil {
		return "", fmt.Errorf("failed to insert credentials: %v", insertErr)
	}
	return stored, nil
}

// Get retrieves password
func (ds *DBStore) Get(groupID string) (string, error) {
	ds.mu.RLock()
	defer ds.mu.RUnlock()

	stmt := ds.preparedStmts["get"]
	var password string
	err := stmt.QueryRow(groupID).Scan(&password)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("credentials not found for group: %s", groupID)
		}
		return "", fmt.Errorf("failed to get credentials: %v", err)
	}

	return password, nil
}

// Delete removes password
func (ds *DBStore) Delete(groupID string) error {
	ds.mu.Lock()
	defer ds.mu.Unlock()

	stmt := ds.preparedStmts["delete"]
	_, err := stmt.Exec(groupID)
	if err != nil {
		return fmt.Errorf("failed to delete credentials: %v", err)
	}

	return nil
}

// ValidatePassword checks password validity (direct comparison, no hash)
func (ds *DBStore) ValidatePassword(groupID string, password string) bool {
	storedPassword, err := ds.Get(groupID)
	if err != nil {
		return false
	}

	return storedPassword == password
}

// GetAll returns all stored credentials
func (ds *DBStore) GetAll() map[string]string {
	ds.mu.RLock()
	defer ds.mu.RUnlock()

	result := make(map[string]string)

	// Table name is validated in NewDBStore, safe to use in query
	query := fmt.Sprintf( // #nosec G201 - table name is validated
		`SELECT group_id, password_hash FROM %s`, ds.tableName)

	rows, err := ds.db.Query(query)
	if err != nil {
		return result
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var groupID, password string
		if err := rows.Scan(&groupID, &password); err != nil {
			continue
		}
		result[groupID] = password
	}

	return result
}

// Close closes the database connection
func (ds *DBStore) Close() error {
	// Close prepared statements
	for _, stmt := range ds.preparedStmts {
		if err := stmt.Close(); err != nil {
			// Log error but continue closing other statements
			// We don't want to stop the cleanup process
			_ = err
		}
	}

	// Close database connection
	return ds.db.Close()
}
