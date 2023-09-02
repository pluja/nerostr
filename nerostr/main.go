package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/pluja/nerostr/api"
	"github.com/pluja/nerostr/db"
	"github.com/pluja/nerostr/monitor"
)

func main() {
	// Logging
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})
	zerolog.SetGlobalLevel(zerolog.InfoLevel)

	// Flags
	apiKey := flag.String("api-key", "", "api key")
	listenAddr := flag.String("listen", ":8080", "server listen address")
	rootDir := flag.String("root", "/app/", "root directory")
	xmrWalletRpc := flag.String("monero-wallet-rpc-url", "http://monero-wallet-rpc:28081/json_rpc", "url for the monero rpc server")
	dev := flag.Bool("dev", false, "development mode")
	testnet := flag.Bool("testnet", false, "use testnet")
	admissionAmount := flag.Float64("admission-amount", 0.002, "admission amount in XMR")
	expireInvoice := flag.Int("expire-invoice", 3600, "expire invoice time in seconds")
	flag.Parse()

	// SET ENVIRONMENT VARIABLES
	if os.Getenv("API_KEY") == "" {
		if *apiKey == "" {
			// Generate random api key
			*apiKey = uuid.New().String()
		}
		log.Info().Msgf("API KEY: %v", *apiKey)
		os.Setenv("API_KEY", *apiKey)
	}

	os.Setenv("TESTNET", "false")
	if *testnet {
		os.Setenv("TESTNET", "true")
	}

	if os.Getenv("MONERO_WALLET_RPC_URL") == "" {
		os.Setenv("MONERO_WALLET_RPC_URL", *xmrWalletRpc)
	}

	if os.Getenv("INVOICE_EXPIRE_TIME") == "" {
		os.Setenv("INVOICE_EXPIRE_TIME", fmt.Sprintf("%v", *expireInvoice))
	}

	if os.Getenv("ADMISSION_AMOUNT") == "" {
		os.Setenv("ADMISSION_AMOUNT", fmt.Sprintf("%v", *admissionAmount))
	}

	if os.Getenv("ROOT_DIR") == "" {
		os.Setenv("ROOT_DIR", *rootDir)
	}

	os.Setenv("DEV", "false")
	if *dev {
		os.Setenv("DEV", "true")
		log.Logger = log.Output(
			zerolog.ConsoleWriter{
				Out:        os.Stdout,
				TimeFormat: "15:04:05",
			},
		).With().Caller().Logger()
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	}

	log.Debug().Msg("DEV MODE IS ON")
	log.Debug().Msgf("ListenAddr: %v", *listenAddr)
	log.Debug().Msgf("RootDir: %v", *rootDir)
	log.Debug().Msgf("AdmissionAmount: %v", *admissionAmount)
	log.Debug().Msgf("MoneroRpcUrl: %v", *xmrWalletRpc)
	log.Debug().Msgf("ExpireInvoice: %v", *expireInvoice)
	log.Debug().Msgf("Testnet: %v", *testnet)

	// SETUP DATABASE

	// Check if nerostr_data/db directory exists in root directory
	if _, err := os.Stat(fmt.Sprintf("%v/nerostr_data/db", *rootDir)); os.IsNotExist(err) {
		log.Debug().Msg("Creating nerostr_data/db directory")
		err := os.MkdirAll(fmt.Sprintf("%v/nerostr_data/db", *rootDir), 0755)
		if err != nil {
			log.Fatal().Err(err).Msg("Error creating nerostr_data/db directory")
		}
	}

	// Create database
	dabs, err := db.NewBadgerDB(fmt.Sprintf("%v/nerostr_data/db", *rootDir))
	if err != nil {
		log.Fatal().Err(err).Msg("Error creating database")
	}
	defer dabs.Close()

	// START SERVER
	server := api.NewServer(*listenAddr, dabs)
	go monitor.MonitorInvoices(server, time.Minute*2, int64(*expireInvoice))
	server.Run()
}
