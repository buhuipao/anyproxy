//go:build dbtest
// +build dbtest

package credential

import (
	"testing"

	// Import database drivers for testing
	_ "modernc.org/sqlite" // Pure Go SQLite driver (no CGO required)
	// _ "github.com/go-sql-driver/mysql" // MySQL driver
	// _ "github.com/lib/pq" // PostgreSQL driver

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDBStore tests the database store implementation
// To run these tests:
// 1. Install the appropriate database driver: go get modernc.org/sqlite
// 2. Run with build tag: go test -tags=dbtest ./...
func TestDBStore(t *testing.T) {
	// Example configuration for SQLite (in-memory)
	config := &DBConfig{
		Driver:     "sqlite", // modernc.org/sqlite uses "sqlite" not "sqlite3"
		DataSource: ":memory:",
		TableName:  "test_credentials",
	}

	// Example configuration for MySQL
	// config := &DBConfig{
	//     Driver:     "mysql",
	//     DataSource: "user:password@tcp(localhost:3306)/testdb",
	//     TableName:  "test_credentials",
	// }

	// Example configuration for PostgreSQL
	// config := &DBConfig{
	//     Driver:     "postgres",
	//     DataSource: "postgres://user:password@localhost/testdb?sslmode=disable",
	//     TableName:  "test_credentials",
	// }

	store, err := NewDBStore(config)
	require.NoError(t, err)
	defer store.Close()

	// Test Set and Get
	t.Run("SetAndGet", func(t *testing.T) {
		err := store.Set("group1", hashPassword("password1"))
		require.NoError(t, err)

		hash, err := store.Get("group1")
		require.NoError(t, err)
		assert.Equal(t, hashPassword("password1"), hash)
	})

	// Test ValidatePassword
	t.Run("ValidatePassword", func(t *testing.T) {
		err := store.Set("group2", hashPassword("password2"))
		require.NoError(t, err)

		valid := store.ValidatePassword("group2", "password2")
		assert.True(t, valid)

		valid = store.ValidatePassword("group2", "wrongpassword")
		assert.False(t, valid)

		valid = store.ValidatePassword("nonexistent", "password")
		assert.False(t, valid)
	})

	// Test Update
	t.Run("Update", func(t *testing.T) {
		err := store.Set("group3", hashPassword("password3"))
		require.NoError(t, err)

		hash1, err := store.Get("group3")
		require.NoError(t, err)

		err = store.Set("group3", hashPassword("newpassword3"))
		require.NoError(t, err)

		hash2, err := store.Get("group3")
		require.NoError(t, err)
		assert.Equal(t, hashPassword("newpassword3"), hash2)
		assert.NotEqual(t, hash1, hash2)
	})

	// Test Delete
	t.Run("Delete", func(t *testing.T) {
		err := store.Set("group4", hashPassword("password4"))
		require.NoError(t, err)

		err = store.Delete("group4")
		require.NoError(t, err)

		_, err = store.Get("group4")
		assert.Error(t, err)
	})

	// Test Multiple Groups
	t.Run("MultipleGroups", func(t *testing.T) {
		err := store.Set("group5", hashPassword("password5"))
		require.NoError(t, err)
		err = store.Set("group6", hashPassword("password6"))
		require.NoError(t, err)

		hash5, err := store.Get("group5")
		require.NoError(t, err)
		assert.Equal(t, hashPassword("password5"), hash5)

		hash6, err := store.Get("group6")
		require.NoError(t, err)
		assert.Equal(t, hashPassword("password6"), hash6)
	})

	// Test Non-existent Group
	t.Run("NonExistentGroup", func(t *testing.T) {
		_, err := store.Get("nonexistent")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "credentials not found for group: nonexistent")
	})

	// Test Empty Table Name
	t.Run("EmptyTableName", func(t *testing.T) {
		config := &DBConfig{
			Driver:     "sqlite",
			DataSource: ":memory:",
			TableName:  "", // Should default to "credentials"
		}

		store, err := NewDBStore(config)
		require.NoError(t, err)
		defer store.Close()

		err = store.Set("test", hashPassword("test"))
		assert.NoError(t, err)
	})
}

func TestDBStore_InvalidConnection(t *testing.T) {
	// Test invalid driver
	t.Run("InvalidDriver", func(t *testing.T) {
		config := &DBConfig{
			Driver:     "invalid_driver",
			DataSource: ":memory:",
		}

		_, err := NewDBStore(config)
		assert.Error(t, err)
	})
}
