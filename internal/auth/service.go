package auth

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

type Service struct {
	jwtSecret     string
	jwtExpiration time.Duration
}

type Claims struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	jwt.RegisteredClaims
}

type User struct {
	ID       string    `json:"id"`
	Username string    `json:"username"`
	Password string    `json:"password"`
	Email    string    `json:"email"`
	Created  time.Time `json:"created"`
	Updated  time.Time `json:"updated"`
}

type LoginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

type RegisterRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
	Email    string `json:"email" binding:"required,email"`
}

type AuthResponse struct {
	Token     string `json:"token"`
	User      User   `json:"user"`
	ExpiresAt int64  `json:"expires_at"`
}

func NewService(jwtSecret string, jwtExpiration time.Duration) *Service {
	return &Service{
		jwtSecret:     jwtSecret,
		jwtExpiration: jwtExpiration,
	}
}

func (s *Service) GenerateToken(user *User) (*AuthResponse, error) {
	expirationTime := time.Now().Add(s.jwtExpiration)
	
	claims := &Claims{
		UserID:   user.ID,
		Username: user.Username,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(expirationTime),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			NotBefore: jwt.NewNumericDate(time.Now()),
			Issuer:    "ancap-web",
			Subject:   user.ID,
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString([]byte(s.jwtSecret))
	if err != nil {
		return nil, err
	}

	return &AuthResponse{
		Token:     tokenString,
		User:      *user,
		ExpiresAt: expirationTime.Unix(),
	}, nil
}

func (s *Service) ValidateToken(tokenString string) (*Claims, error) {
	claims := &Claims{}
	
	token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return []byte(s.jwtSecret), nil
	})

	if err != nil {
		return nil, err
	}

	if !token.Valid {
		return nil, errors.New("invalid token")
	}

	return claims, nil
}

func (s *Service) HashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(bytes), err
}

func (s *Service) CheckPassword(password, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

func (s *Service) CreateUser(req *RegisterRequest) (*User, error) {
	// Hash password
	hashedPassword, err := s.HashPassword(req.Password)
	if err != nil {
		return nil, err
	}

	user := &User{
		ID:       generateID(),
		Username: req.Username,
		Password: hashedPassword,
		Email:    req.Email,
		Created:  time.Now(),
		Updated:  time.Now(),
	}

	return user, nil
}

func (s *Service) AuthenticateUser(req *LoginRequest, storedUser *User) bool {
	return s.CheckPassword(req.Password, storedUser.Password)
}

func generateID() string {
	// TODO: Implementar generación de ID único
	return "user_" + time.Now().Format("20060102150405")
}
