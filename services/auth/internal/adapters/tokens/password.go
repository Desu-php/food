package tokens

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/Desu-php/food/services/auth/internal/domain"
	"golang.org/x/crypto/argon2"
)

type Passwords struct {
	Memory, Iterations uint32
	Parallelism        uint8
	slots              chan struct{}
}

func NewPasswords(memory, iterations uint32, parallelism uint8, concurrency int) (*Passwords, error) {
	if memory < 19*1024 || memory > 256*1024 || iterations < 2 || iterations > 10 || parallelism < 1 || parallelism > 8 || concurrency < 1 || concurrency > 16 {
		return nil, fmt.Errorf("unsafe Argon2id configuration")
	}
	return &Passwords{memory, iterations, parallelism, make(chan struct{}, concurrency)}, nil
}
func (p *Passwords) Hash(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	select {
	case p.slots <- struct{}{}:
	default:
		return "", domain.ErrLimited
	}
	defer func() { <-p.slots }()
	key := argon2.IDKey([]byte(password), salt, p.Iterations, p.Memory, p.Parallelism, 32)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", p.Memory, p.Iterations, p.Parallelism, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}
func (p *Passwords) Verify(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" || len(password) > 1024 {
		return false
	}
	var m, t uint32
	var parallel uint8
	if n, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &parallel); err != nil || n != 3 || m < 19*1024 || m > 256*1024 || t < 2 || t > 10 || parallel < 1 || parallel > 8 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) != 16 {
		return false
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(expected) != 32 {
		return false
	}
	select {
	case p.slots <- struct{}{}:
	default:
		return false
	}
	defer func() { <-p.slots }()
	actual := argon2.IDKey([]byte(password), salt, t, m, parallel, 32)
	return subtle.ConstantTimeCompare(actual, expected) == 1
}
