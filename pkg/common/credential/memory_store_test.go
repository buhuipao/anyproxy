package credential

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMemoryStore(t *testing.T) {
	store := NewMemoryStore()

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
		store := NewMemoryStore() // Fresh store

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
}
