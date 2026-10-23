package application

import (
	"context"
	"strings"

	"github.com/Desu-php/food/services/auth/internal/domain"
)

type AdministrationRepository interface {
	Provision(context.Context, domain.User, string) error
	Import(context.Context, string, []domain.User, string) error
	Disable(context.Context, int64, string) error
}

func (s *Administration) Disable(ctx context.Context, id int64, operator string) error {
	if id <= 0 || !validOperator(operator) {
		return domain.ErrInvalid
	}
	return s.repo.Disable(ctx, id, operator)
}

type Administration struct {
	repo      AdministrationRepository
	passwords Passwords
}

func NewAdministration(repo AdministrationRepository, passwords Passwords) *Administration {
	return &Administration{repo, passwords}
}
func validOperator(operator string) bool {
	return len(strings.TrimSpace(operator)) >= 3 && len(operator) <= 200 && !strings.ContainsAny(operator, "\r\n\x00")
}
func (s *Administration) Provision(ctx context.Context, u domain.User, password, operator string) error {
	u = normalized(u)
	if !validOperator(operator) {
		return domain.ErrInvalid
	}
	email, err := domain.Email(u.Email)
	if err != nil {
		return err
	}
	u.Email = email
	if err = domain.ValidatePassword(password); err != nil {
		return err
	}
	if u.Subject == "" {
		u.Subject = Secret()
	}
	if err = u.ValidateBindings(); err != nil {
		return err
	}
	u.PasswordHash, err = s.passwords.Hash(password)
	if err != nil {
		return err
	}
	u.Status = domain.Active
	return s.repo.Provision(ctx, u, operator)
}
func (s *Administration) Import(ctx context.Context, issuer string, users []domain.User, operator string) error {
	if !validOperator(operator) || issuer == "" || len(users) == 0 {
		return domain.ErrInvalid
	}
	seen := map[int64]bool{}
	subjects := map[string]bool{}
	customers := map[int64]bool{}
	couriers := map[int64]bool{}
	for i, u := range users {
		users[i] = normalized(u)
		if u.ID <= 0 || u.ValidateBindings() != nil || seen[u.ID] || subjects[u.Subject] || u.CustomerID > 0 && customers[u.CustomerID] || u.CourierID > 0 && couriers[u.CourierID] {
			return domain.ErrInvalid
		}
		seen[u.ID] = true
		subjects[u.Subject] = true
		customers[u.CustomerID] = true
		couriers[u.CourierID] = true
	}
	return s.repo.Import(ctx, issuer, users, operator)
}
func normalized(u domain.User) domain.User {
	u.Roles = append([]string{}, u.Roles...)
	u.Permissions = append([]string{}, u.Permissions...)
	u.Restaurants = append([]int64{}, u.Restaurants...)
	return u
}
