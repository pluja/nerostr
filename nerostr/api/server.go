package api

import (
	"html/template"
	"os"
	"path"

	"github.com/goccy/go-json"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/template/html/v2"
	"github.com/rs/zerolog/log"

	"github.com/pluja/nerostr/db"
	monerorpc "github.com/pluja/nerostr/monero-rpc"
)

type Server struct {
	ListenAddr string
	Router     *fiber.App
	Db         db.Db
	NewUserCh  chan bool
	MoneroRpc  *monerorpc.MoneroRpc
}

func NewServer(listenAddr string, db db.Db) *Server {
	// Create a new template engine
	engine := html.New(path.Join(os.Getenv("ROOT_DIR"), "html"), ".html")
	if os.Getenv("DEV") == "true" {
		engine.Reload(true)
	}
	engine.AddFuncMap(
		map[string]interface{}{
			"attr": func(s string) template.HTMLAttr {
				return template.HTMLAttr(s)
			},
			"safe": func(s string) template.HTML {
				return template.HTML(s)
			},
		},
	)
	return &Server{
		ListenAddr: listenAddr,
		Router: fiber.New(fiber.Config{
			JSONEncoder:  json.Marshal,
			JSONDecoder:  json.Unmarshal,
			BodyLimit:    2 * 1024 * 1024, // Increase body limit to 2MB
			ServerHeader: "Fiber",         // Optional, for easier debugging
			Views:        engine,
		}),
		Db:        db,
		NewUserCh: make(chan bool, 100),
		MoneroRpc: monerorpc.NewMoneroRpc(os.Getenv("MONERO_WALLET_RPC_URL")),
	}
}

func (s *Server) Run() {
	s.SetupMiddleware()
	s.RegisterRoutes()
	s.Router.Listen(s.ListenAddr)
}

func (s *Server) SetupMiddleware() {
	s.Router.Use(cors.New())
}

func (s *Server) RegisterRoutes() {
	// Static routes
	s.Router.Static("/static", path.Join(os.Getenv("ROOT_DIR"), "/html/static"), fiber.Static{
		Compress:  true,
		ByteRange: true,
	})

	// Register HTTP route for getting initial state.
	s.Router.Get("/", func(c *fiber.Ctx) error {
		err := s.handleIndexPage(c)
		if err != nil {
			log.Error().Err(err).Msg("Error handling GET /")
		}
		return err
	})

	// Register HTTP route for getting initial state.
	s.Router.Post("/user", func(c *fiber.Ctx) error {
		err := s.handleNewUser(c)
		if err != nil {
			log.Error().Err(err).Msg("Error handling POST /invoice")
		}
		return err
	})

	// Register HTTP route for getting initial state.
	s.Router.Get("/user/:pkey", func(c *fiber.Ctx) error {
		log.Debug().Msgf("GET /user/%v", c.Params("pkey"))
		err := s.handleGetUser(c)
		if err != nil {
			log.Error().Err(err).Msg("Error handling GET /user/:pkey")
		}
		return err
	})

	// Register HTTP route for getting initial state.
	s.Router.Get("/api/status/:pkey", func(c *fiber.Ctx) error {
		log.Debug().Msgf("GET /api/status/%v", c.Params("pkey"))
		err := s.handleGetPkeyStatus(c)
		if err != nil {
			log.Error().Err(err).Msg("Error handling GET /api/status/:pkey")
		}
		return err
	})

	// HTTP route for adding a new user from a pubkey, requiring an API key
	s.Router.Post("/api/user/:pkey", func(c *fiber.Ctx) error {
		log.Debug().Msgf("POST /api/user/%v", c.Params("pkey"))
		err := s.handleApiAddUser(c)
		if err != nil {
			log.Error().Err(err).Msg("Error handling POST /api/user/:pkey")
		}
		return err
	})

	s.Router.Delete("/api/user/:pkey", func(c *fiber.Ctx) error {
		log.Debug().Msgf("DELETE /api/user/%v", c.Params("pkey"))
		err := s.handleApiDeleteUser(c)
		if err != nil {
			log.Error().Err(err).Msg("Error handling DELETE /api/user/:pkey")
		}
		return err
	})

}
