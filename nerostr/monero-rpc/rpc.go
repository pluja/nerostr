package monerorpc

import (
	"os"

	"github.com/monero-ecosystem/go-monero-rpc-client/wallet"
	"github.com/rs/zerolog/log"
)

type MoneroRpc struct {
	Url     string
	User    string
	Pass    string
	Testnet bool
	Client  wallet.Client
}

func NewMoneroRpc(url string) *MoneroRpc {
	testnet := false
	if os.Getenv("TESTNET") == "true" {
		testnet = true
	}

	log.Debug().Msgf("Connecting to monero rpc server: %s", url)
	return &MoneroRpc{
		Url:     url,
		Testnet: testnet,
		Client:  wallet.New(wallet.Config{Address: url}),
	}
}

func (mrpc *MoneroRpc) CreateNewSubaddress(accountIndex uint64, label string) (string, error) {
	// Create a new subaddress
	req := &wallet.RequestCreateAddress{
		AccountIndex: accountIndex,
		Label:        label,
	}
	res, err := mrpc.Client.CreateAddress(req)
	if err != nil {
		return "", err
	}

	// Return the address of the new subaddress
	return res.Address, nil
}

type Transaction struct {
	TxID          string
	Amount        uint64
	Confirmations uint64
}

type Transactions struct {
	TotalConfirmed   uint64
	TotalUnconfirmed uint64
	Transactions     []Transaction
}

func (mrpc *MoneroRpc) GetTransactions(subaddress string) (Transactions, error) {
	addrIndexResponse, err := mrpc.Client.GetAddressIndex(&wallet.RequestGetAddressIndex{
		Address: subaddress,
	})
	if err != nil {
		return Transactions{}, err
	}

	// Get transactions for the subaddress
	addrTransfersResponse, err := mrpc.Client.GetTransfers(&wallet.RequestGetTransfers{
		In:             true,
		Out:            false,
		Pending:        true,
		AccountIndex:   addrIndexResponse.Index.Major,
		SubaddrIndices: []uint64{addrIndexResponse.Index.Minor},
	})
	if err != nil {
		return Transactions{}, err
	}

	var transactions []Transaction
	var totalConfirmed, totalUnconfirmed uint64

	for _, tx := range addrTransfersResponse.In {
		t := Transaction{
			TxID:          tx.TxID,
			Amount:        tx.Amount,
			Confirmations: tx.Confirmations,
		}

		if tx.Confirmations > 0 {
			totalConfirmed += tx.Amount
		} else {
			totalUnconfirmed += tx.Amount
		}

		transactions = append(transactions, t)
	}

	return Transactions{
		TotalConfirmed:   totalConfirmed,
		TotalUnconfirmed: totalUnconfirmed,
		Transactions:     transactions,
	}, nil
}
