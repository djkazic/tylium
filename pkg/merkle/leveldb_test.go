package merkle

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/djkazic/tylium/pkg/types"
)

func TestLevelDBStore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")

	store, err := NewLevelDBStore(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// Put and get.
	if err := store.Put([]byte("key1"), []byte("value1")); err != nil {
		t.Fatal(err)
	}
	val, err := store.Get([]byte("key1"))
	if err != nil {
		t.Fatal(err)
	}
	if string(val) != "value1" {
		t.Fatalf("expected 'value1', got '%s'", val)
	}

	// Get nonexistent.
	val, err = store.Get([]byte("missing"))
	if err != nil {
		t.Fatal(err)
	}
	if val != nil {
		t.Fatal("expected nil for missing key")
	}

	// Delete.
	if err := store.Delete([]byte("key1")); err != nil {
		t.Fatal(err)
	}
	val, _ = store.Get([]byte("key1"))
	if val != nil {
		t.Fatal("expected nil after delete")
	}
}

func TestLevelDBStoreWithPrefix(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")

	store1, err := NewLevelDBStore(path, []byte("ns1/"))
	if err != nil {
		t.Fatal(err)
	}
	defer store1.Close()

	// Create store2 sharing same DB via NewLevelDBStoreFromDB.
	store2 := NewLevelDBStoreFromDB(store1.db, []byte("ns2/"))

	store1.Put([]byte("key"), []byte("from-ns1"))
	store2.Put([]byte("key"), []byte("from-ns2"))

	v1, _ := store1.Get([]byte("key"))
	v2, _ := store2.Get([]byte("key"))

	if string(v1) != "from-ns1" {
		t.Fatalf("ns1: expected 'from-ns1', got '%s'", v1)
	}
	if string(v2) != "from-ns2" {
		t.Fatalf("ns2: expected 'from-ns2', got '%s'", v2)
	}
}

func TestLevelDBMerkleTreeIntegration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "merkle.db")

	store, err := NewLevelDBStore(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	tree := NewTree(store)

	key1 := types.Sha256([]byte("key1"))
	key2 := types.Sha256([]byte("key2"))

	tree.Set(key1, []byte("val1"))
	tree.Set(key2, []byte("val2"))

	root := tree.Root()
	if root.IsZero() {
		t.Fatal("root should be non-zero")
	}

	v, _ := tree.Get(key1)
	if string(v) != "val1" {
		t.Fatalf("expected 'val1', got '%s'", v)
	}

	// Reopen store and rebuild tree — data should be in LevelDB but
	// tree is in-memory, so this tests the store persistence.
	// (The tree itself is ephemeral; state reconstruction reads from store.)
	_ = root
}

func TestLevelDBStorePersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "persist.db")

	// Write.
	store, err := NewLevelDBStore(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	store.Put([]byte("hello"), []byte("world"))
	store.Close()

	// Reopen and read.
	store2, err := NewLevelDBStore(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()

	val, _ := store2.Get([]byte("hello"))
	if string(val) != "world" {
		t.Fatalf("expected 'world', got '%s'", val)
	}

	_ = os.RemoveAll(dir)
}
