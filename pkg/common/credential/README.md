# Credential Package

The credential package provides simple credential management for AnyProxy gateway with support for multiple storage backends.

## Structure

The package has been organized into separate files for better maintainability:

- `credential.go` - Main Manager and Config types
- `memory_store.go` - In-memory storage implementation
- `file_store.go` - File-based storage implementation
- `db_store.go` - Database storage implementation

## Storage Types

### Memory Store
- Stores credentials in memory only
- Fast access
- Data is lost when the application restarts
- Good for development and testing

### File Store
- Stores credentials in a JSON file
- Persists data across restarts
- Good for simple deployments

### DB Store
- Stores credentials in a database
- Supports MySQL, PostgreSQL, SQLite, and other SQL databases
- Good for production deployments with high availability requirements

## Usage

### Configuration

```yaml
# Memory store (default)
credential:
  type: memory

# File store
credential:
  type: file
  file_path: /path/to/credentials.json

# Database store
credential:
  type: db
  db:
    driver: mysql
    data_source: "user:password@tcp(localhost:3306)/dbname"
    table_name: credentials # optional, defaults to "credentials"
```

### Example Code

```go
// Create a manager with memory store
config := &credential.Config{Type: credential.Memory}
mgr, err := credential.NewManager(config)

// Register a group
err = mgr.RegisterGroup("group1", "password123")

// Validate credentials
valid := mgr.ValidateGroup("group1", "password123")

// Remove a group
err = mgr.RemoveGroup("group1")
```

### Database Setup

For the DB store, the table will be automatically created with the following schema:

```sql
CREATE TABLE IF NOT EXISTS credentials (
    group_id VARCHAR(255) PRIMARY KEY,
    password_hash VARCHAR(64) NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
)
```

## Testing

### Running Tests

```bash
# Run all tests except database tests
go test ./...

# Run database tests (requires appropriate driver)
# First install a driver, e.g.:
go get modernc.org/sqlite  # Pure Go SQLite driver

# Then run with the dbtest build tag:
go test -tags=dbtest ./...
```

### Database Drivers

To use the DB store, you need to import the appropriate driver:

```go
// SQLite (Pure Go, no CGO required)
import _ "modernc.org/sqlite"

// MySQL
import _ "github.com/go-sql-driver/mysql"

// PostgreSQL
import _ "github.com/lib/pq"
``` 