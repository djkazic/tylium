package rpc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/djkazic/tylium/internal/state"
	"github.com/djkazic/tylium/pkg/merkle"
	"github.com/djkazic/tylium/pkg/types"
)

type mockNodeInfo struct {
	height    uint64
	stateRoot types.Hash256
}

func (m *mockNodeInfo) Height() uint64              { return m.height }
func (m *mockNodeInfo) StateRoot() types.Hash256    { return m.stateRoot }
func (m *mockNodeInfo) FinalizedHeight() uint64     { return 0 }
func (m *mockNodeInfo) FinalizedRoot() types.Hash256 { return types.ZeroHash }

func setupTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	stateDB := state.NewStateDB(merkle.NewMemStore())
	info := &mockNodeInfo{height: 42, stateRoot: types.Sha256([]byte("test"))}
	handler := NewHandler(info, stateDB, nil)
	srv := NewServer(handler)

	if err := srv.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Stop() })
	return srv, fmt.Sprintf("http://%s", srv.Addr())
}

func rpcCall(t *testing.T, url, method string, params interface{}) json.RawMessage {
	t.Helper()
	if params == nil {
		params = []interface{}{}
	}
	body, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0", "id": 1, "method": method, "params": params,
	})
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	var rpcResp struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	json.Unmarshal(raw, &rpcResp)
	if rpcResp.Error != nil {
		t.Fatalf("rpc error: %s", rpcResp.Error.Message)
	}
	return rpcResp.Result
}

func TestBlockHeight(t *testing.T) {
	_, url := setupTestServer(t)
	result := rpcCall(t, url, "tyl_blockHeight", nil)

	var hr BlockHeightResult
	json.Unmarshal(result, &hr)
	if hr.Height != 42 {
		t.Fatalf("expected height 42, got %d", hr.Height)
	}
}

func TestStateRoot(t *testing.T) {
	_, url := setupTestServer(t)
	result := rpcCall(t, url, "tyl_stateRoot", nil)

	var sr StateRootResult
	json.Unmarshal(result, &sr)
	expected := types.Sha256([]byte("test")).Hex()
	if sr.StateRoot != expected {
		t.Fatalf("expected %s, got %s", expected, sr.StateRoot)
	}
}

func TestGetAccountNotFound(t *testing.T) {
	_, url := setupTestServer(t)
	result := rpcCall(t, url, "tyl_getAccount", []string{"0000000000000000000000000000000000000001"})

	var ar AccountResult
	json.Unmarshal(result, &ar)
	if ar.Balance != 0 {
		t.Fatalf("expected balance 0 for nonexistent account")
	}
}
