package db

import "github.com/pluja/nerostr/models"

type Db interface {
	NewUser(user models.User) error
	GetUser(pubkey string) (models.User, error)
	UpdateUser(user models.User) error
	DeleteUser(pubkey string) error
	GetNewUsers() ([]models.User, error)
	GetUserCount() (int, error)
	Close() error
}
