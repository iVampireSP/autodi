package user

import (
	"example.com/testapp/internal/cache"
	"example.com/testapp/internal/db"
)

// Service provides user CRUD operations.
type Service struct {
	db    *db.DB
	cache *cache.Cache
}

// NewUser creates a UserService.
func NewUser(db *db.DB, cache *cache.Cache) *Service {
	return &Service{db: db, cache: cache}
}

// Create adds a new user.
func (s *Service) Create(name string) error {
	_ = s.db.Query("INSERT INTO users ...")
	s.cache.Set("user:"+name, name)
	return nil
}

// Find looks up a user by ID.
func (s *Service) Find(id string) (string, error) {
	if v := s.cache.Get("user:" + id); v != "" {
		return v, nil
	}
	rows := s.db.Query("SELECT * FROM users WHERE id=?")
	if len(rows) > 0 {
		return rows[0], nil
	}
	return "", nil
}
