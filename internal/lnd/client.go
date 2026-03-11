package lnd

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

// Client communicates with LND via its REST API.
type Client struct {
	endpoint string
	macaroon string // hex-encoded macaroon
	http     *http.Client
}

// NewClient creates an LND REST client.
func NewClient(endpoint, tlsCertPath, macaroonPath string) (*Client, error) {
	certPEM, err := os.ReadFile(tlsCertPath)
	if err != nil {
		return nil, fmt.Errorf("read tls cert: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certPEM) {
		return nil, fmt.Errorf("failed to parse tls cert")
	}

	macBytes, err := os.ReadFile(macaroonPath)
	if err != nil {
		return nil, fmt.Errorf("read macaroon: %w", err)
	}

	return &Client{
		endpoint: endpoint,
		macaroon: hex.EncodeToString(macBytes),
		http: &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					RootCAs: pool,
				},
			},
		},
	}, nil
}

// InvoiceState represents LND invoice states.
type InvoiceState int

const (
	InvoiceOpen     InvoiceState = 0
	InvoiceSettled  InvoiceState = 1
	InvoiceCanceled InvoiceState = 2
	InvoiceAccepted InvoiceState = 3
)

// NodeInfo is the response from /v1/getinfo.
type NodeInfo struct {
	PubKey string `json:"identity_pubkey"`
	Alias  string `json:"alias"`
}

// GetInfo returns basic node info (connectivity check).
func (c *Client) GetInfo() (*NodeInfo, error) {
	var info NodeInfo
	if err := c.get("/v1/getinfo", &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// AddHoldInvoice creates a hold invoice locked to the given hash.
func (c *Client) AddHoldInvoice(hash [32]byte, amountSats int64, memo string) (string, error) {
	body := map[string]interface{}{
		"hash":  base64.StdEncoding.EncodeToString(hash[:]),
		"value": amountSats,
		"memo":  memo,
	}
	var resp struct {
		PaymentRequest string `json:"payment_request"`
	}
	if err := c.post("/v2/invoices/hodl", body, &resp); err != nil {
		return "", err
	}
	return resp.PaymentRequest, nil
}

// SettleInvoice settles a hold invoice with the given preimage.
func (c *Client) SettleInvoice(preimage [32]byte) error {
	body := map[string]interface{}{
		"preimage": base64.StdEncoding.EncodeToString(preimage[:]),
	}
	return c.post("/v2/invoices/settle", body, nil)
}

// CancelInvoice cancels a hold invoice.
func (c *Client) CancelInvoice(hash [32]byte) error {
	body := map[string]interface{}{
		"payment_hash": base64.StdEncoding.EncodeToString(hash[:]),
	}
	return c.post("/v2/invoices/cancel", body, nil)
}

// LookupInvoice checks the state of an invoice by hash.
func (c *Client) LookupInvoice(hash [32]byte) (InvoiceState, error) {
	hashB64 := base64.URLEncoding.EncodeToString(hash[:])
	var resp struct {
		State string `json:"state"`
	}
	if err := c.get("/v2/invoices/lookup?payment_hash="+hashB64, &resp); err != nil {
		return -1, err
	}
	switch resp.State {
	case "OPEN":
		return InvoiceOpen, nil
	case "SETTLED":
		return InvoiceSettled, nil
	case "CANCELED":
		return InvoiceCanceled, nil
	case "ACCEPTED":
		return InvoiceAccepted, nil
	default:
		return -1, fmt.Errorf("unknown invoice state: %s", resp.State)
	}
}

// DecodedInvoice holds the decoded fields of a BOLT11 payment request.
type DecodedInvoice struct {
	PaymentHash string `json:"payment_hash"`
	NumSatoshis int64  `json:"num_satoshis,string"`
	Description string `json:"description"`
}

// DecodePayReq decodes a BOLT11 payment request and returns its details.
func (c *Client) DecodePayReq(payReq string) (*DecodedInvoice, error) {
	var resp DecodedInvoice
	if err := c.get("/v1/payreq/"+payReq, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// SendPayment pays a Lightning invoice and returns the preimage on success.
func (c *Client) SendPayment(payReq string, timeoutSecs int) ([32]byte, error) {
	body := map[string]interface{}{
		"payment_request": payReq,
		"timeout_seconds": timeoutSecs,
		"no_inflight_updates": true,
	}

	respBody, err := c.postRaw("/v2/router/send", body)
	if err != nil {
		return [32]byte{}, err
	}

	// The response is streamed JSON. Parse the last result with a status.
	var result struct {
		Result struct {
			Status        string `json:"status"`
			PreimageHex   string `json:"payment_preimage"`
			FailureReason string `json:"failure_reason"`
		} `json:"result"`
	}

	// Read all streamed lines, use the last one.
	dec := json.NewDecoder(bytes.NewReader(respBody))
	for dec.More() {
		if err := dec.Decode(&result); err != nil {
			break
		}
	}

	if result.Result.Status != "SUCCEEDED" {
		return [32]byte{}, fmt.Errorf("payment failed: %s (%s)", result.Result.Status, result.Result.FailureReason)
	}

	preimageBytes, err := base64.StdEncoding.DecodeString(result.Result.PreimageHex)
	if err != nil || len(preimageBytes) != 32 {
		// Fall back to hex decoding.
		preimageBytes, err = hex.DecodeString(result.Result.PreimageHex)
		if err != nil || len(preimageBytes) != 32 {
			return [32]byte{}, fmt.Errorf("invalid preimage in response")
		}
	}
	var preimage [32]byte
	copy(preimage[:], preimageBytes)
	return preimage, nil
}

func (c *Client) get(path string, out interface{}) error {
	req, _ := http.NewRequest("GET", c.endpoint+path, nil)
	req.Header.Set("Grpc-Metadata-macaroon", c.macaroon)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("lnd request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("lnd %s: HTTP %d: %s", path, resp.StatusCode, body)
	}
	if out != nil {
		return json.Unmarshal(body, out)
	}
	return nil
}

func (c *Client) post(path string, payload interface{}, out interface{}) error {
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", c.endpoint+path, bytes.NewReader(body))
	req.Header.Set("Grpc-Metadata-macaroon", c.macaroon)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("lnd request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("lnd %s: HTTP %d: %s", path, resp.StatusCode, respBody)
	}
	if out != nil {
		return json.Unmarshal(respBody, out)
	}
	return nil
}

func (c *Client) postRaw(path string, payload interface{}) ([]byte, error) {
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", c.endpoint+path, bytes.NewReader(body))
	req.Header.Set("Grpc-Metadata-macaroon", c.macaroon)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("lnd request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lnd %s: HTTP %d: %s", path, resp.StatusCode, respBody)
	}
	return respBody, nil
}
