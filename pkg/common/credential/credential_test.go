package credential

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManager(t *testing.T) {
	// Test with memory store
	t.Run("MemoryStore", func(t *testing.T) {
		mgr, err := NewManager(&Config{Type: Memory})
		require.NoError(t, err)

		testManagerOperations(t, mgr)
	})

	// Test with file store
	t.Run("FileStore", func(t *testing.T) {
		tempDir := t.TempDir()
		filePath := filepath.Join(tempDir, "test_credentials.json")

		mgr, err := NewManager(&Config{
			Type:     File,
			FilePath: filePath,
		})
		require.NoError(t, err)

		testManagerOperations(t, mgr)

		// Verify file exists
		_, err = os.Stat(filePath)
		assert.NoError(t, err)
	})

	// Test default configuration
	t.Run("DefaultConfig", func(t *testing.T) {
		mgr, err := NewManager(nil)
		require.NoError(t, err)
		assert.NotNil(t, mgr.store)
	})

	// Test invalid store type
	t.Run("InvalidStoreType", func(t *testing.T) {
		_, err := NewManager(&Config{Type: "invalid"})
		assert.Error(t, err)
	})

	// Test DB store configuration
	t.Run("DBStoreConfig", func(t *testing.T) {
		// Test missing DB config
		_, err := NewManager(&Config{Type: DB})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "database configuration is required")
	})
}

func testManagerOperations(t *testing.T, mgr *Manager) {
	// Test RegisterGroup
	t.Run("RegisterGroup", func(t *testing.T) {
		err := mgr.RegisterGroup("testgroup", "testpassword")
		require.NoError(t, err)

		// Test duplicate registration with same password (should succeed)
		err = mgr.RegisterGroup("testgroup", "testpassword")
		require.NoError(t, err)

		// Test empty group ID or password
		err = mgr.RegisterGroup("", "password")
		assert.Error(t, err)

		err = mgr.RegisterGroup("group", "")
		assert.Error(t, err)
	})

	// Test ValidateGroup
	t.Run("ValidateGroup", func(t *testing.T) {
		valid := mgr.ValidateGroup("testgroup", "testpassword")
		assert.True(t, valid)

		valid = mgr.ValidateGroup("testgroup", "wrongpassword")
		assert.False(t, valid)

		valid = mgr.ValidateGroup("nonexistent", "password")
		assert.False(t, valid)

		// Test empty inputs
		valid = mgr.ValidateGroup("", "password")
		assert.False(t, valid)

		valid = mgr.ValidateGroup("group", "")
		assert.False(t, valid)
	})

	// Test RemoveGroup
	t.Run("RemoveGroup", func(t *testing.T) {
		err := mgr.RegisterGroup("toremove", "password")
		require.NoError(t, err)

		err = mgr.RemoveGroup("toremove")
		require.NoError(t, err)

		valid := mgr.ValidateGroup("toremove", "password")
		assert.False(t, valid)
	})

	// Test Update Password
	t.Run("UpdatePassword", func(t *testing.T) {
		err := mgr.RegisterGroup("updatetest", "oldpassword")
		require.NoError(t, err)

		valid := mgr.ValidateGroup("updatetest", "oldpassword")
		assert.True(t, valid)

		err = mgr.RegisterGroup("updatetest", "newpassword")
		require.NoError(t, err)

		valid = mgr.ValidateGroup("updatetest", "oldpassword")
		assert.False(t, valid)

		valid = mgr.ValidateGroup("updatetest", "newpassword")
		assert.True(t, valid)
	})
}

func TestHashPassword(t *testing.T) {
	// Test consistent hashing
	password := "testpassword123"
	hash1 := hashPassword(password)
	hash2 := hashPassword(password)
	assert.Equal(t, hash1, hash2)

	// Test different passwords produce different hashes
	hash3 := hashPassword("differentpassword")
	assert.NotEqual(t, hash1, hash3)

	// Test hash format
	assert.Len(t, hash1, 64) // SHA256 produces 32 bytes = 64 hex characters
}

func TestConcurrency(t *testing.T) {
	mgr, err := NewManager(&Config{Type: Memory})
	require.NoError(t, err)

	// Test concurrent operations
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(id int) {
			groupID := fmt.Sprintf("group%d", id)
			password := fmt.Sprintf("password%d", id)

			err := mgr.RegisterGroup(groupID, password)
			assert.NoError(t, err)

			valid := mgr.ValidateGroup(groupID, password)
			assert.True(t, valid)

			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Verify all groups can be validated
	for i := 0; i < 10; i++ {
		groupID := fmt.Sprintf("group%d", i)
		password := fmt.Sprintf("password%d", i)
		valid := mgr.ValidateGroup(groupID, password)
		assert.True(t, valid)
	}
}
