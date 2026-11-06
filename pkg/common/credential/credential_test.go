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

	// A registered password cannot be replaced by another one
	t.Run("RejectDifferentPassword", func(t *testing.T) {
		err := mgr.RegisterGroup("updatetest", "oldpassword")
		require.NoError(t, err)

		valid := mgr.ValidateGroup("updatetest", "oldpassword")
		assert.True(t, valid)

		err = mgr.RegisterGroup("updatetest", "newpassword")
		assert.Error(t, err)

		valid = mgr.ValidateGroup("updatetest", "oldpassword")
		assert.True(t, valid)

		valid = mgr.ValidateGroup("updatetest", "newpassword")
		assert.False(t, valid)

		// After the group is removed, a new password can be registered
		require.NoError(t, mgr.RemoveGroup("updatetest"))
		require.NoError(t, mgr.RegisterGroup("updatetest", "newpassword"))
		assert.True(t, mgr.ValidateGroup("updatetest", "newpassword"))
	})
}

// Store errors (file or DB driver details) are sent to the remote client, so the
// returned error must not contain them
func TestRegisterGroupHidesStoreErrors(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "credentials.json")
	mgr, err := NewManager(&Config{Type: File, FilePath: filePath})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filePath, []byte("not json"), 0600))

	err = mgr.RegisterGroup("group1", "password1")
	require.Error(t, err)
	assert.Equal(t, "authentication failed: credential store unavailable", err.Error())
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
