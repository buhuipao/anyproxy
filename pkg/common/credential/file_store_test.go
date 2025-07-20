package credential

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileStore(t *testing.T) {
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "test_credentials.json")

	store, err := NewFileStore(filePath)
	require.NoError(t, err)

	// Test Set and Get
	t.Run("SetAndGet", func(t *testing.T) {
		err := store.Set("group1", hashPassword("password1"))
		require.NoError(t, err)

		hash, err := store.Get("group1")
		require.NoError(t, err)
		assert.Equal(t, hashPassword("password1"), hash)
	})

	// Test persistence
	t.Run("Persistence", func(t *testing.T) {
		// Create new store instance with same file
		store2, err := NewFileStore(filePath)
		require.NoError(t, err)

		hash, err := store2.Get("group1")
		require.NoError(t, err)
		assert.Equal(t, hashPassword("password1"), hash)
	})

	// Test ValidatePassword
	t.Run("ValidatePassword", func(t *testing.T) {
		valid := store.ValidatePassword("group1", "password1")
		assert.True(t, valid)

		valid = store.ValidatePassword("group1", "wrongpassword")
		assert.False(t, valid)
	})

	// Test Delete
	t.Run("Delete", func(t *testing.T) {
		err := store.Delete("group1")
		require.NoError(t, err)

		_, err = store.Get("group1")
		assert.Error(t, err)
	})

	// Test invalid file path
	t.Run("InvalidPath", func(t *testing.T) {
		_, err := NewFileStore("/invalid/path/credentials.json")
		assert.Error(t, err)
	})
}
