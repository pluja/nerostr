package api

import (
	"os"
	"strconv"

	"github.com/gofiber/fiber/v2"
	"github.com/rs/zerolog/log"

	"github.com/pluja/nerostr/db"
	"github.com/pluja/nerostr/models"
	"github.com/pluja/nerostr/utils"
)

func (s *Server) handleNewUser(c *fiber.Ctx) error {
	// Get pubkey from form value
	pubkey := c.FormValue("pkey")
	if pubkey == "" {
		log.Error().Msg("Pubkey is required")
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "pkey is required",
		})
	}

	// Check if user exists in database
	user, err := s.Db.GetUser(pubkey)
	if err != nil {
		log.Warn().Err(err).Msg("getting user")
	}

	// If user exists, redirect to /user/:pkey
	if user.PubKey != "" {
		log.Debug().Msgf("User exists: %v", user.PubKey)
		return c.Redirect("/user/" + pubkey)
	}

	// Set the pubkey
	err = user.SetPubKey(pubkey)
	if err != nil {
		log.Error().Err(err).Msg("Error setting pubkey")
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	// Create a new subaddress for the payment
	subaddress, err := s.MoneroRpc.CreateNewSubaddress(0, user.PubKey)
	if err != nil {
		log.Error().Err(err).Msg("Error creating new subaddress")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": err.Error(),
		})
	}
	log.Debug().Msgf("New subaddress: %v", subaddress)
	user.Address = subaddress

	// Set status to new
	user.Status = models.UserStatusNew

	// Set date to now
	user.SetDateNow()

	// Set amount
	amount, err := strconv.ParseFloat(os.Getenv("ADMISSION_AMOUNT"), 64)
	if err != nil {
		log.Error().Err(err).Msg("Error parsing default amount")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": err.Error(),
		})
	}
	user.SetAmount(amount)

	// If user does not exist, create new user
	err = s.Db.NewUser(user)
	if err != nil {
		log.Error().Err(err).Msg("Error creating new user")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	log.Debug().Msgf("New user created: %v", user.PubKey)
	return c.Redirect("/user/" + user.PubKey)
}

func (s *Server) handleGetUser(c *fiber.Ctx) error {
	// Get pubkey from url param
	pubkey := c.Params("pkey")
	if pubkey == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "pkey is required",
		})
	}

	npub, err := utils.PrasePubKey(pubkey)
	if err != nil {
		log.Debug().Err(err).Msg("Error parsing pubkey")
		return c.Redirect("/")
	}

	// Check if user exists in database
	user, err := s.Db.GetUser(npub)
	if err != nil {
		log.Debug().Err(err).Msg("Error getting user")
		return c.Redirect("/")
	}

	// If user does not exist, redirect to /
	if user.PubKey == "" {
		return c.Redirect("/")
	}

	// If user exists, render user page
	return c.Render("user", fiber.Map{
		"Title": "Nerostr User Page",
		"User":  &user,
	}, "base")
}

func (s *Server) handleIndexPage(c *fiber.Ctx) error {
	return c.Render("index", fiber.Map{
		"Title": "Nerostr Relay",
	}, "base")
}

func (s *Server) handleGetPkeyStatus(c *fiber.Ctx) error {
	// Get pubkey from url param
	pubkey := c.Params("pkey")
	if pubkey == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "pkey is required",
		})
	}

	npub, err := utils.PrasePubKey(pubkey)
	if err != nil {
		log.Debug().Err(err).Msg("Error parsing pubkey")
		return c.JSON(fiber.Map{
			"status": "error",
			"action": "reject",
		})
	}

	// Check if user exists in database
	user, err := s.Db.GetUser(npub)
	if err != nil {
		log.Debug().Err(err).Msg("Error getting user")
		return c.JSON(fiber.Map{
			"status": user.Status,
			"action": "reject",
		})
	}

	log.Debug().Msgf("User status: %v", user.Status)
	if user.Status == models.UserStatusPaid {
		return c.JSON(fiber.Map{
			"status": user.Status,
			"action": "accept",
		})
	} else {
		return c.JSON(fiber.Map{
			"status": user.Status,
			"action": "reject",
		})
	}
}

func (s *Server) handleApiAddUser(c *fiber.Ctx) error {
	// Get public key from URL param
	pkey := c.Params("pkey")
	// GET API key from header "X-API-KEY"
	apikey := c.Get("X-API-KEY")

	// Check if API key is set
	if apikey == "" {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "apikey is required",
		})
	}

	// Check if API key is valid
	if apikey != os.Getenv("API_KEY") {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "invalid apikey",
		})
	}

	npub, err := utils.PrasePubKey(pkey)
	if err != nil {
		log.Debug().Err(err).Msg("Error parsing pubkey")
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "error parsing pubkey",
		})
	}

	// Check if user exists in database
	user, err := s.Db.GetUser(npub)
	if err != nil {
		log.Debug().Err(err).Msg("Error getting user")
	}

	// If user exists, redirect to /user/:pkey
	if user.PubKey != "" {
		log.Debug().Msgf("User exists: %v", user.PubKey)
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "user already exists",
		})
	}

	// Add new user
	err = addNewPubkey(pkey, s.Db)
	if err != nil {
		log.Debug().Err(err).Msg("Error adding new user")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "error adding new user",
		})
	}

	return c.JSON(fiber.Map{
		"status": "ok",
	})
}

func (s *Server) handleApiDeleteUser(c *fiber.Ctx) error {
	// Get public key from URL param
	pkey := c.Params("pkey")

	// GET API key from header "X-API-KEY"
	apikey := c.Get("X-API-KEY")

	// Check if API key is set
	if apikey == "" {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "apikey is required",
		})
	}

	// Check if API key is valid
	if apikey != os.Getenv("API_KEY") {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "invalid apikey",
		})
	}

	npub, err := utils.PrasePubKey(pkey)
	if err != nil {
		log.Debug().Err(err).Msg("Error parsing pubkey")
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "error parsing pubkey",
		})
	}

	// Delete user
	err = s.Db.DeleteUser(npub)
	if err != nil {
		log.Debug().Err(err).Msg("Error deleting user")
		return c.JSON(fiber.Map{
			"error": "error deleting user",
		})
	}

	return c.JSON(fiber.Map{
		"status": "ok",
	})
}

func addNewPubkey(pk string, d db.Db) error {
	newUser := models.User{
		Status: models.UserStatusPaid,
	}
	err := newUser.SetPubKey(pk)
	if err != nil {
		return err
	}
	newUser.SetDateNow()
	newUser.Address = "manually_added"
	newUser.TxHash = "manually_added"
	newUser.Amount = 0.0
	err = d.NewUser(newUser)
	if err != nil {
		return err
	}
	return nil
}
