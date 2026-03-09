package merkle

import (
	"testing"

	"github.com/djkazic/tylium/pkg/types"
)

func TestTreeBasicOps(t *testing.T) {
	tree := NewTree(NewMemStore())

	key := types.Sha256([]byte("hello"))
	val := []byte("world")

	// Empty tree should return nil.
	got, err := tree.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %v", got)
	}

	// Set and get.
	if err := tree.Set(key, val); err != nil {
		t.Fatal(err)
	}
	got, err = tree.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "world" {
		t.Fatalf("expected 'world', got '%s'", got)
	}

	// Root should be non-zero.
	root := tree.Root()
	if root.IsZero() {
		t.Fatal("expected non-zero root")
	}

	// Delete and verify.
	if err := tree.Delete(key); err != nil {
		t.Fatal(err)
	}
	got, err = tree.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected nil after delete, got %v", got)
	}
}

func TestTreeMultipleKeys(t *testing.T) {
	tree := NewTree(NewMemStore())

	pairs := map[string]string{
		"key1": "value1",
		"key2": "value2",
		"key3": "value3",
		"key4": "value4",
	}

	for k, v := range pairs {
		key := types.Sha256([]byte(k))
		tree.Set(key, []byte(v))
	}

	// Verify all present.
	for k, v := range pairs {
		key := types.Sha256([]byte(k))
		got, _ := tree.Get(key)
		if string(got) != v {
			t.Fatalf("key %s: expected '%s', got '%s'", k, v, got)
		}
	}

	// Delete one and verify others remain.
	delKey := types.Sha256([]byte("key2"))
	tree.Delete(delKey)

	got, _ := tree.Get(delKey)
	if got != nil {
		t.Fatal("expected nil for deleted key")
	}

	// Others still there.
	for k, v := range pairs {
		if k == "key2" {
			continue
		}
		key := types.Sha256([]byte(k))
		got, _ := tree.Get(key)
		if string(got) != v {
			t.Fatalf("key %s: expected '%s', got '%s'", k, v, got)
		}
	}
}

func TestTreeDeterministicRoot(t *testing.T) {
	// Two trees with same data should produce same root.
	tree1 := NewTree(NewMemStore())
	tree2 := NewTree(NewMemStore())

	keys := []string{"alpha", "beta", "gamma"}
	for _, k := range keys {
		key := types.Sha256([]byte(k))
		val := []byte("val-" + k)
		tree1.Set(key, val)
		tree2.Set(key, val)
	}

	if tree1.Root() != tree2.Root() {
		t.Fatalf("roots differ: %s vs %s", tree1.Root().Hex(), tree2.Root().Hex())
	}
}

func TestTreeUpdate(t *testing.T) {
	tree := NewTree(NewMemStore())
	key := types.Sha256([]byte("mutable"))

	tree.Set(key, []byte("v1"))
	root1 := tree.Root()

	tree.Set(key, []byte("v2"))
	root2 := tree.Root()

	if root1 == root2 {
		t.Fatal("root should change after update")
	}

	got, _ := tree.Get(key)
	if string(got) != "v2" {
		t.Fatalf("expected 'v2', got '%s'", got)
	}
}

func TestFlushAndReload(t *testing.T) {
	store := NewMemStore()
	tree := NewTree(store)

	pairs := map[string]string{
		"key1": "value1",
		"key2": "value2",
		"key3": "value3",
	}
	for k, v := range pairs {
		tree.Set(types.Sha256([]byte(k)), []byte(v))
	}

	root := tree.Root()
	if err := tree.Flush(); err != nil {
		t.Fatal(err)
	}

	// Create a new tree from the same store, loading the persisted root.
	tree2 := NewTree(store)
	tree2.LoadRoot(root)

	if tree2.Root() != root {
		t.Fatalf("root mismatch after reload: %s vs %s", tree2.Root().Hex(), root.Hex())
	}

	// Verify all values.
	for k, v := range pairs {
		got, err := tree2.Get(types.Sha256([]byte(k)))
		if err != nil {
			t.Fatalf("key %s: %v", k, err)
		}
		if string(got) != v {
			t.Fatalf("key %s: expected '%s', got '%s'", k, v, got)
		}
	}

	// Verify non-existent key.
	got, err := tree2.Get(types.Sha256([]byte("nope")))
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatal("expected nil for missing key")
	}
}

func TestFlushAndModify(t *testing.T) {
	store := NewMemStore()
	tree := NewTree(store)

	key1 := types.Sha256([]byte("a"))
	key2 := types.Sha256([]byte("b"))
	tree.Set(key1, []byte("1"))
	tree.Set(key2, []byte("2"))
	tree.Flush()
	root1 := tree.Root()

	// Reload and modify.
	tree2 := NewTree(store)
	tree2.LoadRoot(root1)
	tree2.Set(key1, []byte("updated"))
	tree2.Flush()
	root2 := tree2.Root()

	if root1 == root2 {
		t.Fatal("root should change after modification")
	}

	// Reload again and verify.
	tree3 := NewTree(store)
	tree3.LoadRoot(root2)
	got, _ := tree3.Get(key1)
	if string(got) != "updated" {
		t.Fatalf("expected 'updated', got '%s'", got)
	}
	got, _ = tree3.Get(key2)
	if string(got) != "2" {
		t.Fatalf("expected '2', got '%s'", got)
	}
}

func TestProofVerification(t *testing.T) {
	tree := NewTree(NewMemStore())
	key := types.Sha256([]byte("provable"))
	tree.Set(key, []byte("data"))

	root := tree.Root()
	proof, err := tree.Prove(key)
	if err != nil {
		t.Fatal(err)
	}

	if !VerifyProof(root, proof, DefaultHashFunc) {
		t.Fatal("valid proof failed verification")
	}
}
