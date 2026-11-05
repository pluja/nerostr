package wallet

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/icholy/digest"
)

// Client talks to monero-wallet-rpc's JSON-RPC endpoint.
type Client struct {
	url  string
	http *http.Client
}

func New(url, user, password string) *Client {
	var transport http.RoundTripper = &http.Transport{
		MaxIdleConns:        4,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	if user != "" {
		transport = &digest.Transport{Username: user, Password: password, Transport: transport}
	}
	return &Client{url: url, http: &http.Client{Transport: transport, Timeout: 60 * time.Second}}
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("wallet rpc error %d: %s", e.Code, e.Message) }

const maxResponseBytes = 32 << 20

func (c *Client) call(ctx context.Context, method string, params, result any) error {
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "0", "method": method, "params": params})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("wallet rpc %s: %w", method, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("wallet rpc %s: http %s", method, resp.Status)
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&envelope); err != nil {
		return fmt.Errorf("wallet rpc %s: decoding response: %w", method, err)
	}
	if envelope.Error != nil {
		return envelope.Error
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(envelope.Result, result); err != nil {
		return fmt.Errorf("wallet rpc %s: decoding result: %w", method, err)
	}
	return nil
}

// Version returns the wallet RPC version as (major, minor).
func (c *Client) Version(ctx context.Context) (major, minor uint32, err error) {
	var r struct {
		Version uint32 `json:"version"`
	}
	if err := c.call(ctx, "get_version", struct{}{}, &r); err != nil {
		return 0, 0, err
	}
	return r.Version >> 16, r.Version & 0xffff, nil
}

// Height is the wallet's synced height, which lags the daemon while it catches up.
func (c *Client) Height(ctx context.Context) (uint64, error) {
	var r struct {
		Height uint64 `json:"height"`
	}
	err := c.call(ctx, "get_height", struct{}{}, &r)
	return r.Height, err
}

// CreateAddress creates a new subaddress in account 0.
func (c *Client) CreateAddress(ctx context.Context, label string) (address string, index uint32, err error) {
	var r struct {
		Address      string `json:"address"`
		AddressIndex uint32 `json:"address_index"`
	}
	err = c.call(ctx, "create_address", map[string]any{"account_index": 0, "label": label}, &r)
	if err == nil && r.Address == "" {
		err = fmt.Errorf("wallet rpc create_address: empty address")
	}
	return r.Address, r.AddressIndex, err
}

type Transfer struct {
	TxID            string `json:"txid"`
	Amount          uint64 `json:"amount"`
	Confirmations   uint64 `json:"confirmations"`
	Height          uint64 `json:"height"`
	DoubleSpendSeen bool   `json:"double_spend_seen"`
	Type            string `json:"type"`
	SubaddrIndex    struct {
		Major uint32 `json:"major"`
		Minor uint32 `json:"minor"`
	} `json:"subaddr_index"`
}

// IncomingTransfers lists confirmed transfers above minHeight plus mempool
// transfers to the given subaddresses of account 0.
func (c *Client) IncomingTransfers(ctx context.Context, subaddrIndices []uint32, minHeight uint64) ([]Transfer, error) {
	var r struct {
		In   []Transfer `json:"in"`
		Pool []Transfer `json:"pool"`
	}
	params := map[string]any{
		"in":               true,
		"pool":             true,
		"account_index":    0,
		"subaddr_indices":  subaddrIndices,
		"filter_by_height": true,
		"min_height":       minHeight,
	}
	if err := c.call(ctx, "get_transfers", params, &r); err != nil {
		return nil, err
	}
	return append(r.In, r.Pool...), nil
}
