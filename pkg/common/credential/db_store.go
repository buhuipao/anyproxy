package credential

import (
	"database/sql"
	"fmt"
	"regexp"
	"sync"
)

// DBStore implements database-based credential storage
type DBStore struct {
	db            *sql.DB
	tableName     string
	mu            sync.RWMutex
	preparedStmts map[string]*sql.Stmt
}

// DBConfig holds database configuration
type DBConfig struct {
	Driver     string // Database driver: mysql, postgres, sqlite3
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

	db, err := sql.Open(config.Driver, config.DataSource)
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
		stmt, err := ds.db.Prepare(query)
		if err != nil {
			return fmt.Errorf("failed to prepare %s statement: %v", name, err)
		}
		ds.preparedStmts[name] = stmt
	}

	return nil
}

// Set stores or updates password hash
func (ds *DBStore) Set(groupID string, passwordHash string) error {
	ds.mu.Lock()
	defer ds.mu.Unlock()

	// First try to update
	// Table name is validated in NewDBStore, safe to use in query
	updateQuery := fmt.Sprintf( // #nosec G201 - table name is validated
		`UPDATE %s SET password_hash = ?, updated_at = CURRENT_TIMESTAMP WHERE group_id = ?`,
		ds.tableName)

	result, err := ds.db.Exec(updateQuery, passwordHash, groupID)
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

		_, err = ds.db.Exec(insertQuery, groupID, passwordHash)
		if err != nil {
			return fmt.Errorf("failed to insert credentials: %v", err)
		}
	}

	return nil
}

// Get retrieves password hash
func (ds *DBStore) Get(groupID string) (string, error) {
	ds.mu.RLock()
	defer ds.mu.RUnlock()

	stmt := ds.preparedStmts["get"]
	var passwordHash string
	err := stmt.QueryRow(groupID).Scan(&passwordHash)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("credentials not found for group: %s", groupID)
		}
		return "", fmt.Errorf("failed to get credentials: %v", err)
	}

	return passwordHash, nil
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

// ValidatePassword checks password validity
func (ds *DBStore) ValidatePassword(groupID string, password string) bool {
	hash, err := ds.Get(groupID)
	if err != nil {
		return false
	}

	return hash == hashPassword(password)
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
