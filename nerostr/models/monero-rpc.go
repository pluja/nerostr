package models

type RpcSubAddressCommand struct {
	Jsonrpc string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  struct {
		AccountIndex int    `json:"account_index"`
		Label        string `json:"label,omitempty"`
	} `json:"params"`
	Id string `json:"id"`
}
